package app.shuku.wear

import android.annotation.SuppressLint
import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.graphics.drawable.Icon
import android.media.AudioAttributes
import android.media.AudioDeviceCallback
import android.media.AudioDeviceInfo
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.media.MediaMetadata
import android.media.MediaPlayer
import android.media.session.MediaSession
import android.media.session.PlaybackState
import android.os.Bundle
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.os.SystemClock
import android.provider.Settings
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import android.widget.Toast
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.security.MessageDigest
import java.util.Locale
import java.util.concurrent.CompletableFuture
import java.util.concurrent.CompletionException
import java.util.concurrent.Executors
import kotlin.math.max
import kotlin.math.min

/**
 * 在手表上听书：把这一章按句切开，用书库的自然语音（POST /api/tts，和手机、网页听书同一个音色）合成，
 * 手表自己播放——蓝牙耳机，或者用户同意后用手表扬声器。书库的语音用不了（连不上书库、设置成了设备语音）
 * 时改用手表自带的语音。读完一章接着读下一章；进度和阅读页一样传回书库，手机、网页能接着读。
 * 锁屏、表盘上的媒体控制、耳机按键都能控制（MediaSession）。
 */
class ListenService : Service() {
    private class Chap(val i: Int, val data: JSONObject, val title: String, val len: Int)
    private class Seg(val chap: Chap, val n: Int, val text: String, val start: Int)

    /** 给界面看的状态 */
    class State(
        val active: Boolean,
        val bookId: Long = 0,
        val title: String = "",
        val chapter: String = "",
        val text: String = "",
        val playing: Boolean = false,
        val needOutput: Boolean = false,
        val status: String = "",
        val speed: Double = 1.0,
        val timer: Int = 0,
        val timerLeftMs: Long = 0,
        val chapterIndex: Int = 0,
        val frac: Double = 0.0,
        val fileId: Long = 0,
        val deviceVoice: Boolean = false,
    )

    private val handler = Handler(Looper.getMainLooper())
    private val synthPool = Executors.newFixedThreadPool(2)
    private val io = Executors.newSingleThreadExecutor()
    private val app get() = WearApp.app

    private lateinit var session: MediaSession
    private lateinit var audio: AudioManager
    private var focusRequest: AudioFocusRequest? = null
    private var wakeLock: PowerManager.WakeLock? = null

    private var bookId = 0L
    private var title = ""
    private var author = ""
    private var fileId = 0L
    private var chapters = JSONArray()
    private var files: BookFiles? = null
    private var startChap = 0
    private var startFrac = 0.0

    private val queue = ArrayList<Seg>()
    private var index = 0
    private var lastChap = -1 // 已经取到的最后一章
    private var playing = false
    private var needOutput = false
    private var loadingNext = false
    private var waitingNext = false
    private var noMore = false
    private var status = ""
    private var speed = 1.0
    private var timerMin = 0 // 0 不定时，>0 分钟，-1 读完本章
    private var timerEnd = 0L
    private var pausedByFocus = false
    private var stopped = false
    private var gen = 0 // 每次重新开始（换书、换位置）加一，旧的后台结果作废
    private var playToken = 0

    // 书库语音用不了时改用手表自带语音；不是设置成设备语音的话，读几十句后再试一次书库
    private var useDevice = false
    private var deviceOnly = false
    private var deviceCount = 0
    private var serverError = ""
    private var errorsInRow = 0

    private var player: MediaPlayer? = null
    private var playerSeg: Seg? = null
    private val audioJobs = HashMap<Seg, CompletableFuture<File>>()
    private var tts: TextToSpeech? = null
    private var ttsReady = false
    private var utterance: String? = null

    private var listenSince = 0L
    private var lastSave = 0L
    private var savedChap = -1

    // ---------- 生命周期 ----------

    override fun onCreate() {
        super.onCreate()
        instance = this
        speed = app.config.prefs.getFloat("listen_speed", 1f).toDouble()
        audio = getSystemService(AUDIO_SERVICE) as AudioManager
        session = MediaSession(this, "shuku-watch-listen")
        session.setCallback(object : MediaSession.Callback() {
            override fun onPlay() = resume()
            override fun onPause() = userPause()
            override fun onSkipToNext() = step(1)
            override fun onSkipToPrevious() = step(-1)
            override fun onStop() = stop("")
        })
        session.setSessionActivity(openPlayer())
        // startForegroundService 之后 5 秒内必须调用 startForeground
        goForeground()
        registerReceiver(noisy, IntentFilter(AudioManager.ACTION_AUDIO_BECOMING_NOISY))
        audio.registerAudioDeviceCallback(devices, handler)
        File(cacheDir, "tts").deleteRecursively() // 上次没清掉的
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACT_START -> {
                goForeground()
                begin(intent.getLongExtra("id", 0), intent.getStringExtra("title") ?: "", intent.getIntExtra("i", 0), intent.getDoubleExtra("frac", 0.0))
            }
            ACT_PLAY -> resume()
            ACT_PAUSE -> userPause()
            ACT_NEXT -> step(1)
            ACT_PREV -> step(-1)
            ACT_STOP -> stop("")
        }
        if (bookId == 0L) stop("") // 只是点了通知上的按钮，书已经关了
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        if (!stopped) {
            if (playing) saveProgress()
            stopped = true
        }
        if (instance === this) instance = null
        gen++
        try { unregisterReceiver(noisy) } catch (e: Exception) {}
        audio.unregisterAudioDeviceCallback(devices)
        handler.removeCallbacksAndMessages(null)
        releasePlayer()
        tts?.shutdown()
        session.release()
        focusRequest?.let { audio.abandonAudioFocusRequest(it) }
        releaseLock()
        synthPool.shutdownNow()
        io.shutdownNow()
        super.onDestroy()
    }

    // 拔掉耳机、蓝牙耳机断开：暂停，不要突然从手表扬声器外放
    private val noisy = object : BroadcastReceiver() {
        override fun onReceive(c: Context?, i: Intent?) = userPause()
    }

    private val devices = object : AudioDeviceCallback() {
        override fun onAudioDevicesAdded(added: Array<out AudioDeviceInfo>) {
            // 等着连耳机：连上了就开始读
            if (needOutput && AudioOut.ok(this@ListenService)) resume()
        }

        override fun onAudioDevicesRemoved(removed: Array<out AudioDeviceInfo>) {
            if (playing && !AudioOut.ok(this@ListenService)) userPause()
        }
    }

    // ---------- 开始 ----------

    private fun begin(id: Long, bookTitle: String, i: Int, frac: Double) {
        if (id == 0L) return
        stopped = false
        instance = this
        lastReason = ""
        val my = ++gen
        releasePlayer()
        tts?.stop()
        clearAudio()
        queue.clear()
        index = 0
        lastChap = -1
        loadingNext = false
        waitingNext = false
        noMore = false
        needOutput = false
        errorsInRow = 0
        if (id != bookId) {
            chapters = JSONArray()
            fileId = 0
            author = ""
        }
        bookId = id
        title = bookTitle
        val f = BookFiles(this, id)
        files = f
        startChap = i
        startFrac = frac
        handler.removeCallbacks(idleStop)
        status = "准备中…"
        playing = true
        pausedByFocus = false
        session.isActive = true
        requestFocus()
        acquireLock()
        listenSince = SystemClock.elapsedRealtime()
        publish()
        pausePhone()
        io.execute {
            val r = runCatching {
                // 阅读页刚取过目录，一般不用再联网
                val toc = f.recentToc(10 * 60_000L)
                val chs = toc.optJSONArray("chapters") ?: JSONArray()
                if (chs.length() == 0) throw IOException("这本书没有可以朗读的文字")
                val ci = i.coerceIn(0, chs.length() - 1)
                Triple(toc, ci, f.chapter(ci, toc.optLong("file_id"), chs.optJSONObject(ci)))
            }
            handler.post {
                if (my != gen || stopped) return@post
                val (toc, ci, ch) = r.getOrElse {
                    if (it is WearApp.Unauthorized) {
                        stop(it.message)
                        return@post
                    }
                    playing = false
                    listenSince = 0
                    status = "打开失败：" + (it.message ?: "连不上书库")
                    releaseLock()
                    idleLater(10 * 60_000L)
                    publish()
                    return@post
                }
                fileId = toc.optLong("file_id")
                chapters = toc.optJSONArray("chapters") ?: JSONArray()
                if (title.isEmpty()) title = toc.optString("title")
                author = toc.optString("author")
                val segs = segsOf(ci, ch)
                lastChap = ci
                queue.addAll(segs)
                // 从 frac 所在的那一句开始
                val at = (frac * (segs.firstOrNull()?.chap?.len ?: 0)).toInt()
                index = segs.indexOfLast { it.start <= at }.coerceAtLeast(0)
                status = ""
                if (playing) playCurrent() else publish()
            }
        }
    }

    private fun segsOf(i: Int, ch: JSONObject): List<Seg> {
        val text = BookText.format(ch.optString("text"))
        val chap = Chap(i, ch, ch.optString("title"), text.length)
        return BookText.sentences(text).mapIndexed { n, s -> Seg(chap, n, s.text, s.start) }
    }

    /** 手机上也在读：先让手机停下，不要两边一起出声 */
    private fun pausePhone() {
        val s = runCatching { JSONObject(app.config.str("tts_state", "")) }.getOrNull() ?: return
        val fresh = System.currentTimeMillis() - app.config.prefs.getLong("tts_at", 0) < 3 * 3600_000L
        if (fresh && s.optBoolean("active") && s.optBoolean("playing")) app.io.execute { PhoneLink.send(this, "/shuku/tts/pause") }
    }

    // ---------- 播放 ----------

    private fun playCurrent() {
        if (!playing || stopped) return
        val seg = queue.getOrNull(index)
        if (seg == null) {
            chapterEnd()
            return
        }
        onSegment(seg)
        if (!AudioOut.ok(this)) {
            // 没连耳机：先不出声，等用户连好耳机（或者同意用手表扬声器）。趁这会儿先合成
            playing = false
            needOutput = true
            status = ""
            listenSince = 0
            releaseLock()
            if (!useDevice) {
                audioFor(seg)
                prefetch()
            }
            idleLater(10 * 60_000L)
            publish()
            return
        }
        needOutput = false
        acquireLock()
        moreText()
        if (useDevice && !deviceOnly && deviceCount >= 30) {
            // 用手表语音读了一阵：再试试书库的语音
            useDevice = false
            deviceCount = 0
        }
        if (useDevice) {
            deviceCount++
            speakDevice(seg)
            return
        }
        val job = audioFor(seg)
        if (!job.isDone) {
            status = "合成中…"
            publish()
        }
        prefetch()
        val my = ++playToken // 同一句等合成时又点了继续：只按最后一次的来，不重复开播
        job.whenComplete { f, e ->
            handler.post {
                if (my != playToken || !playing || stopped || queue.getOrNull(index) !== seg) return@post
                if (f != null) startPlayer(seg, f) else fallback(if (e is CompletionException) e.cause ?: e else e)
            }
        }
    }

    private fun startPlayer(seg: Seg, file: File) {
        releasePlayer()
        try {
            val p = MediaPlayer()
            p.setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
            p.setWakeMode(this, PowerManager.PARTIAL_WAKE_LOCK)
            p.setDataSource(file.path)
            p.setOnCompletionListener { if (player === p) advance() }
            p.setOnErrorListener { _, _, _ ->
                if (player === p) {
                    releasePlayer()
                    file.delete()
                    if (++errorsInRow >= 3) pauseAt("音频无法播放") else advance()
                }
                true
            }
            p.prepare()
            p.start()
            player = p
            playerSeg = seg
            errorsInRow = 0
            status = ""
            publish()
        } catch (e: Exception) {
            file.delete()
            fallback(e)
        }
    }

    /** 合成一句（后台线程）：同一句、同一语速只合成一次 */
    private fun audioFor(seg: Seg): CompletableFuture<File> = audioJobs.getOrPut(seg) {
        val s = speed
        CompletableFuture.supplyAsync({
            val f = File(cacheDir, "tts/${sha1("$s|${seg.text}")}.mp3")
            if (f.exists() && f.length() > 0) return@supplyAsync f
            val r = app.api("POST", "/api/tts", JSONObject().put("text", seg.text).put("voice", "").put("speed", s), 60000)
            if (!r.ok) throw IOException(r.errorMessage())
            f.parentFile?.mkdirs()
            val tmp = File(f.path + ".part")
            tmp.writeBytes(r.body)
            if (!tmp.renameTo(f)) throw IOException("存不下语音")
            f
        }, synthPool)
    }

    // 先合成后面三句，句子之间不停顿；读过的删掉，手表空间小
    private fun prefetch() {
        for (k in 1..3) queue.getOrNull(index + k)?.let { audioFor(it) }
        val keep = (index - 1..index + 3).mapNotNull { queue.getOrNull(it) }.toSet()
        val it = audioJobs.entries.iterator()
        while (it.hasNext()) {
            val (seg, job) = it.next()
            if (seg in keep) continue
            if (job.isDone && !job.isCompletedExceptionally) runCatching { job.get().delete() }
            job.cancel(false)
            it.remove()
        }
    }

    private fun clearAudio() {
        audioJobs.values.forEach { it.cancel(false) }
        audioJobs.clear()
    }

    private fun advance() {
        if (!playing) return
        val prev = queue.getOrNull(index)
        index++
        val next = queue.getOrNull(index)
        if (timerMin == -1 && prev != null && next?.chap !== prev.chap) {
            // 定时“读完本章”：这一章读完了
            timerMin = 0
            pauseAt("本章读完了")
            return
        }
        trimQueue()
        playCurrent()
    }

    private fun trimQueue() {
        if (index < 400) return
        val n = index - 50
        queue.subList(0, n).clear()
        index -= n
    }

    /** 取到的句子读完了：接着取下一章，全书读完就停 */
    private fun chapterEnd() {
        moreText(force = true)
        if (noMore && !loadingNext) {
            saveProgress(done = true)
            playing = false // 上面已经存成读完了，stop 里不要再按最后一句存一遍
            stop("全书读完了")
            return
        }
        waitingNext = true
        status = "加载下一章…"
        publish()
    }

    /** 快读完已经取到的句子时，取下一章接上 */
    private fun moreText(force: Boolean = false) {
        if (loadingNext || noMore || bookId == 0L) return
        if (!force && (queue.size - index > 8 || timerMin == -1)) return
        val ni = lastChap + 1
        if (ni >= chapters.length()) {
            noMore = true
            return
        }
        val f = files ?: return
        loadingNext = true
        val my = gen
        val fid = fileId
        val meta = chapters.optJSONObject(ni)
        io.execute {
            val r = runCatching { f.chapter(ni, fid, meta) }
            if (r.isSuccess) runCatching { f.prune(ni, fid) }
            handler.post {
                if (my != gen || stopped) return@post
                loadingNext = false
                r.onSuccess { ch ->
                    lastChap = ni
                    queue.addAll(segsOf(ni, ch))
                    if (waitingNext) {
                        waitingNext = false
                        status = ""
                        playCurrent()
                    }
                }.onFailure {
                    if (waitingNext) {
                        waitingNext = false
                        pauseAt("下一章没取到：" + (it.message ?: "连不上书库"))
                    }
                }
            }
        }
    }

    private fun step(d: Int) {
        if (stopped || bookId == 0L) return
        if (queue.isEmpty() && chapters.length() == 0) {
            resume() // 还没打开成功：重试
            return
        }
        // 可以跳到最后一句之后：接着取下一章
        index = (index + d).coerceIn(0, queue.size)
        if (d < 0 && index >= queue.size) index = max(0, queue.size - 1)
        playing = true
        needOutput = false
        pausedByFocus = false
        status = ""
        handler.removeCallbacks(idleStop)
        releasePlayer()
        tts?.stop()
        requestFocus()
        if (listenSince == 0L) listenSince = SystemClock.elapsedRealtime()
        playCurrent()
        publish()
    }

    private fun userPause() {
        pausedByFocus = false
        pauseAt("")
    }

    private fun pauseAt(why: String) {
        if (stopped) return
        needOutput = false
        waitingNext = false
        status = why
        if (playing) {
            playing = false
            player?.takeIf { it.isPlaying }?.pause()
            tts?.stop()
            saveProgress()
        }
        listenSince = 0
        releaseLock()
        idleLater(30 * 60_000L)
        publish()
    }

    // 暂停（或者等着连耳机）太久没再听：关掉，不一直挂着通知
    private val idleStop = Runnable { if (!playing) stop("") }

    private fun idleLater(ms: Long) {
        handler.removeCallbacks(idleStop)
        handler.postDelayed(idleStop, ms)
    }

    private fun resume() {
        if (playing || stopped || bookId == 0L) return
        if (chapters.length() == 0) {
            // 上次没打开成功：重试
            begin(bookId, title, startChap, startFrac)
            return
        }
        handler.removeCallbacks(idleStop)
        playing = true
        needOutput = false
        status = ""
        requestFocus()
        listenSince = SystemClock.elapsedRealtime()
        val p = player
        val seg = queue.getOrNull(index)
        if (p != null && seg != null && playerSeg === seg && !useDevice && AudioOut.ok(this)) {
            p.start()
            acquireLock()
            publish()
        } else {
            releasePlayer()
            playCurrent()
            publish()
        }
    }

    private fun stop(reason: String?) {
        if (stopped) return
        if (playing) saveProgress()
        stopped = true
        lastReason = reason ?: ""
        if (instance === this) instance = null
        gen++
        playing = false
        releasePlayer()
        tts?.stop()
        handler.removeCallbacks(timerTask)
        handler.removeCallbacks(idleStop)
        session.isActive = false
        focusRequest?.let { audio.abandonAudioFocusRequest(it) }
        releaseLock()
        clearAudio()
        File(cacheDir, "tts").deleteRecursively()
        sendBroadcast(Intent(WearApp.ACTION_LISTEN).setPackage(packageName))
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun releasePlayer() {
        player?.let {
            try { it.stop() } catch (e: Exception) {}
            it.release()
        }
        player = null
        playerSeg = null
    }

    /** 书库语音用不了（连不上书库、设置成了设备语音、微软的服务连不上）：改用手表自带语音 */
    private fun fallback(e: Throwable?) {
        if (e is WearApp.Unauthorized) {
            stop(e.message)
            return
        }
        if (useDevice) {
            pauseAt("朗读失败")
            return
        }
        serverError = e?.message ?: "合成失败"
        deviceOnly = serverError.contains("设备自带语音")
        useDevice = true
        deviceCount = 0
        playCurrent()
    }

    // ---------- 手表自带语音 ----------

    private fun speakDevice(seg: Seg) {
        val t = tts
        if (t == null || !ttsReady) {
            status = "准备手表语音…"
            publish()
            if (t == null) tts = TextToSpeech(this) { st ->
                handler.post {
                    ttsReady = st == TextToSpeech.SUCCESS
                    if (!ttsReady) {
                        tts?.shutdown()
                        tts = null
                        deviceTrouble("手表上没有可用的语音引擎")
                        return@post
                    }
                    tts?.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
                        override fun onStart(id: String?) {}
                        override fun onDone(id: String?) {
                            handler.post { if (id == utterance) { errorsInRow = 0; advance() } }
                        }

                        @Deprecated("Deprecated in Java")
                        override fun onError(id: String?) {
                            handler.post { if (id == utterance) { if (++errorsInRow >= 3) pauseAt("手表语音朗读失败") else advance() } }
                        }
                    })
                    if (playing && useDevice) queue.getOrNull(index)?.let { speakDevice(it) }
                }
            }
            return
        }
        val loc = localeOf(seg.text)
        if (t.voice?.locale?.language != loc.language && t.setLanguage(loc) < TextToSpeech.LANG_AVAILABLE) {
            deviceTrouble("手表自带语音不会读这种语言")
            return
        }
        t.setSpeechRate(speed.toFloat())
        utterance = "$gen:${seg.chap.i}:${seg.n}"
        t.speak(seg.text, TextToSpeech.QUEUE_FLUSH, Bundle(), utterance)
        status = ""
        publish()
    }

    /** 两种语音都用不了 */
    private fun deviceTrouble(why: String) {
        val server = if (serverError.isNotEmpty() && !deviceOnly) "书库语音：$serverError；" else ""
        useDevice = false
        pauseAt(server + why)
    }

    private fun localeOf(text: String): Locale {
        var han = 0
        var kana = 0
        var hangul = 0
        for (c in text) {
            when (Character.UnicodeScript.of(c.code)) {
                Character.UnicodeScript.HAN -> han++
                Character.UnicodeScript.HIRAGANA, Character.UnicodeScript.KATAKANA -> kana++
                Character.UnicodeScript.HANGUL -> hangul++
                else -> {}
            }
        }
        return when {
            kana > 0 -> Locale.JAPANESE
            hangul > han -> Locale.KOREAN
            han > 0 -> Locale.SIMPLIFIED_CHINESE
            else -> Locale.US
        }
    }

    // ---------- 语速、定时 ----------

    private fun cycleSpeed() {
        val i = SPEEDS.indexOfFirst { it >= speed - 0.01 }
        speed = SPEEDS[(i + 1) % SPEEDS.size]
        app.config.prefs.edit().putFloat("listen_speed", speed.toFloat()).apply()
        clearAudio()
        releasePlayer()
        if (playing) {
            tts?.stop()
            playCurrent()
        }
        publish()
    }

    private val timerTask = Runnable {
        timerMin = 0
        timerEnd = 0
        pauseAt("定时结束")
    }

    private fun cycleTimer() {
        timerMin = TIMERS[(TIMERS.indexOf(timerMin) + 1) % TIMERS.size]
        handler.removeCallbacks(timerTask)
        timerEnd = 0
        if (timerMin > 0) {
            timerEnd = System.currentTimeMillis() + timerMin * 60_000L
            handler.postDelayed(timerTask, timerMin * 60_000L)
        }
        publish()
    }

    // ---------- 进度 ----------

    /** 读到新的一句：手表上记下位置（阅读页跟着翻），每半分钟、换章时传回书库 */
    private fun onSegment(seg: Seg) {
        val frac = seg.start.toDouble() / max(1, seg.chap.len)
        app.config.prefs.edit().putString("pos_$bookId", JSONObject().put("file", fileId).put("i", seg.chap.i).put("frac", frac)
            .put("by", "listen").put("at", System.currentTimeMillis()).toString()).apply()
        if (seg.chap.i != savedChap || SystemClock.elapsedRealtime() - lastSave > 30_000) saveProgress()
    }

    private fun saveProgress(done: Boolean = false) {
        if (fileId == 0L) return
        val seg = queue.getOrNull(index) ?: queue.lastOrNull() ?: return
        val frac = if (done) 1.0 else seg.start.toDouble() / max(1, seg.chap.len)
        val now = SystemClock.elapsedRealtime()
        val secs = if (listenSince > 0) ((now - listenSince) / 1000).toInt() else 0
        if (listenSince > 0) listenSince = now
        lastSave = now
        savedChap = seg.chap.i
        val body = JSONObject().put("file_id", fileId).put("location", BookPos.location(chapters, seg.chap.i, frac))
            .put("percent", BookPos.percent(seg.chap.data, frac, done)).put("device", WearApp.DEVICE).put("seconds", min(600, secs))
        val id = bookId
        app.io.execute { app.saveProgress(id, body) }
    }

    // ---------- 音频焦点、锁 ----------

    private fun requestFocus() {
        val req = focusRequest ?: AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
            .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
            .setOnAudioFocusChangeListener({ change ->
                when (change) {
                    AudioManager.AUDIOFOCUS_LOSS -> userPause()
                    AudioManager.AUDIOFOCUS_LOSS_TRANSIENT, AudioManager.AUDIOFOCUS_LOSS_TRANSIENT_CAN_DUCK -> if (playing) {
                        pauseAt("")
                        pausedByFocus = true
                    }
                    AudioManager.AUDIOFOCUS_GAIN -> if (pausedByFocus) {
                        pausedByFocus = false
                        resume()
                    }
                }
            }, handler).build().also { focusRequest = it }
        audio.requestAudioFocus(req)
    }

    private fun acquireLock() {
        val w = wakeLock ?: (getSystemService(POWER_SERVICE) as PowerManager).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "shuku:listen")
            .apply { setReferenceCounted(false) }.also { wakeLock = it }
        w.acquire(3 * 3600_000L)
    }

    private fun releaseLock() {
        try { wakeLock?.takeIf { it.isHeld }?.release() } catch (e: Exception) {}
    }

    // ---------- 状态：界面、表盘的媒体控制、通知 ----------

    private fun snapshot(): State {
        val seg = queue.getOrNull(index)
        return State(
            active = true, bookId = bookId, title = title, chapter = seg?.chap?.title ?: "", text = seg?.text ?: "",
            playing = playing, needOutput = needOutput, status = status, speed = speed, timer = timerMin,
            timerLeftMs = if (timerEnd > 0) max(0, timerEnd - System.currentTimeMillis()) else 0,
            chapterIndex = seg?.chap?.i ?: startChap, frac = seg?.let { it.start.toDouble() / max(1, it.chap.len) } ?: startFrac,
            fileId = fileId, deviceVoice = useDevice,
        )
    }

    private fun publish() {
        if (stopped) return
        val seg = queue.getOrNull(index)
        val actions = PlaybackState.ACTION_PLAY or PlaybackState.ACTION_PAUSE or PlaybackState.ACTION_PLAY_PAUSE or
            PlaybackState.ACTION_SKIP_TO_NEXT or PlaybackState.ACTION_SKIP_TO_PREVIOUS or PlaybackState.ACTION_STOP
        session.setPlaybackState(PlaybackState.Builder().setActions(actions)
            .setState(if (playing) PlaybackState.STATE_PLAYING else PlaybackState.STATE_PAUSED, PlaybackState.PLAYBACK_POSITION_UNKNOWN, if (playing) 1f else 0f).build())
        session.setMetadata(MediaMetadata.Builder()
            .putString(MediaMetadata.METADATA_KEY_TITLE, title.ifEmpty { "书库 · 听书" })
            .putString(MediaMetadata.METADATA_KEY_ARTIST, author)
            .putString(MediaMetadata.METADATA_KEY_ALBUM, seg?.chap?.title ?: "")
            .putString(MediaMetadata.METADATA_KEY_DISPLAY_SUBTITLE, seg?.text?.take(60) ?: "")
            .build())
        getSystemService(NotificationManager::class.java)?.notify(NOTIF_ID, notification())
        sendBroadcast(Intent(WearApp.ACTION_LISTEN).setPackage(packageName))
    }

    private fun goForeground() {
        startForeground(NOTIF_ID, notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK)
    }

    private fun openPlayer(): PendingIntent =
        PendingIntent.getActivity(this, 0, Intent(this, ListenActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)

    private fun action(icon: Int, label: String, act: String, code: Int) = Notification.Action.Builder(
        Icon.createWithResource(this, icon), label,
        PendingIntent.getService(this, code, Intent(this, ListenService::class.java).setAction(act), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)).build()

    private fun notification(): Notification {
        val seg = queue.getOrNull(index)
        val line = when {
            needOutput -> "请连接蓝牙耳机"
            status.isNotEmpty() -> status
            else -> seg?.text ?: "准备中…"
        }
        return Notification.Builder(this, WearApp.CH_LISTEN)
            .setSmallIcon(android.R.drawable.ic_lock_silent_mode_off)
            .setContentTitle(title.ifEmpty { "书库 · 听书" })
            .setContentText(line)
            .setContentIntent(openPlayer())
            .setDeleteIntent(PendingIntent.getService(this, 9, Intent(this, ListenService::class.java).setAction(ACT_STOP), PendingIntent.FLAG_IMMUTABLE))
            .setOngoing(playing)
            .setVisibility(Notification.VISIBILITY_PUBLIC)
            .setCategory(Notification.CATEGORY_TRANSPORT)
            .addAction(action(android.R.drawable.ic_media_previous, "上一句", ACT_PREV, 1))
            .addAction(if (playing) action(android.R.drawable.ic_media_pause, "暂停", ACT_PAUSE, 2) else action(android.R.drawable.ic_media_play, "继续", ACT_PLAY, 3))
            .addAction(action(android.R.drawable.ic_media_next, "下一句", ACT_NEXT, 4))
            .setStyle(Notification.MediaStyle().setMediaSession(session.sessionToken).setShowActionsInCompactView(0, 1, 2))
            .build()
    }

    companion object {
        @Volatile var instance: ListenService? = null
            private set

        /** 上次为什么停了（全书读完了、令牌失效），播放页显示 */
        @Volatile var lastReason = ""

        private const val NOTIF_ID = 8
        const val ACT_START = "app.shuku.wear.LISTEN"
        const val ACT_PLAY = "app.shuku.wear.PLAY"
        const val ACT_PAUSE = "app.shuku.wear.PAUSE"
        const val ACT_NEXT = "app.shuku.wear.NEXT"
        const val ACT_PREV = "app.shuku.wear.PREV"
        const val ACT_STOP = "app.shuku.wear.STOP"
        val SPEEDS = doubleArrayOf(0.8, 1.0, 1.2, 1.4, 1.6, 1.8, 2.0)
        val TIMERS = intArrayOf(0, 15, 30, 60, -1)

        /** 从第 i 章的 frac 处开始读 */
        fun start(ctx: Context, id: Long, title: String, i: Int, frac: Double) {
            lastReason = ""
            ctx.startForegroundService(Intent(ctx, ListenService::class.java).setAction(ACT_START)
                .putExtra("id", id).putExtra("title", title).putExtra("i", i).putExtra("frac", frac))
        }

        // 下面这些都在主线程调用（界面的按钮）
        fun state(): State = instance?.snapshot() ?: State(false, status = lastReason)

        /** 正在听这本书（包括暂停着） */
        fun listening(bookId: Long): Boolean = instance?.let { it.bookId == bookId && !it.stopped } ?: false

        fun toggle() {
            instance?.let { if (it.playing) it.userPause() else it.resume() }
        }

        fun next() { instance?.step(1) }
        fun prev() { instance?.step(-1) }
        fun stopNow() { instance?.stop("") }
        fun cycleSpeed() { instance?.cycleSpeed() }
        fun cycleTimer() { instance?.cycleTimer() }

        /** 用户同意用手表扬声器读 */
        fun useSpeaker() {
            AudioOut.speakerOk = true
            instance?.resume()
        }

        private fun sha1(s: String): String = MessageDigest.getInstance("SHA-1").digest(s.toByteArray()).joinToString("") { "%02x".format(it) }
    }
}

/** 手表的声音从哪儿出：蓝牙耳机（Wear OS 推荐），或者用户同意后用手表扬声器 */
object AudioOut {
    @SuppressLint("InlinedApi")
    private val EXTERNAL = setOf(
        AudioDeviceInfo.TYPE_BLUETOOTH_A2DP, AudioDeviceInfo.TYPE_BLE_HEADSET, AudioDeviceInfo.TYPE_BLE_SPEAKER, AudioDeviceInfo.TYPE_BLE_BROADCAST,
        AudioDeviceInfo.TYPE_WIRED_HEADPHONES, AudioDeviceInfo.TYPE_WIRED_HEADSET, AudioDeviceInfo.TYPE_USB_HEADSET,
    )

    private fun outputs(ctx: Context): Array<AudioDeviceInfo> =
        (ctx.getSystemService(Context.AUDIO_SERVICE) as AudioManager).getDevices(AudioManager.GET_DEVICES_OUTPUTS)

    /** 连着蓝牙耳机或音箱 */
    fun external(ctx: Context) = outputs(ctx).any { it.type in EXTERNAL }

    /** 手表有扬声器 */
    fun hasSpeaker(ctx: Context) = ctx.packageManager.hasSystemFeature(PackageManager.FEATURE_AUDIO_OUTPUT) &&
        outputs(ctx).any { it.type == AudioDeviceInfo.TYPE_BUILTIN_SPEAKER }

    /** 用户同意过用手表扬声器读 */
    var speakerOk: Boolean
        get() = WearApp.app.config.bool("listen_speaker", false)
        set(v) {
            WearApp.app.config.prefs.edit().putBoolean("listen_speaker", v).apply()
        }

    fun ok(ctx: Context) = external(ctx) || (speakerOk && hasSpeaker(ctx))

    /** 去连蓝牙耳机：Wear OS 5 起用系统的“输出设备”面板，没有就打开蓝牙设置 */
    fun choose(ctx: Context) {
        val pm = ctx.packageManager
        fun system(i: Intent): ComponentName? = pm.queryIntentActivities(i, 0).firstOrNull {
            val ai = it.activityInfo?.applicationInfo
            ai != null && ai.flags and (ApplicationInfo.FLAG_SYSTEM or ApplicationInfo.FLAG_UPDATED_SYSTEM_APP) != 0
        }?.activityInfo?.let { ComponentName(it.packageName, it.name) }
        val panel = Intent("com.android.settings.panel.action.MEDIA_OUTPUT")
            .putExtra("com.android.settings.panel.extra.PACKAGE_NAME", ctx.packageName)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        system(panel)?.let { c -> if (runCatching { ctx.startActivity(panel.setComponent(c)) }.isSuccess) return }
        val bt = Intent(Settings.ACTION_BLUETOOTH_SETTINGS)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK)
            .putExtra("EXTRA_CONNECTION_ONLY", true)
            .putExtra("EXTRA_CLOSE_ON_CONNECT", true)
            .putExtra("android.bluetooth.devicepicker.extra.FILTER_TYPE", 1)
        system(bt)?.let { bt.component = it }
        if (runCatching { ctx.startActivity(bt) }.isFailure) Toast.makeText(ctx, "请在手表的“设置 → 连接 → 蓝牙”里连接耳机", Toast.LENGTH_LONG).show()
    }
}
