package app.shuku

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.drawable.Icon
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.media.MediaMetadata
import android.media.MediaPlayer
import android.media.session.MediaSession
import android.media.session.PlaybackState
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.os.SystemClock
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import app.shuku.core.BookPos
import app.shuku.core.BookText
import app.shuku.core.Http
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.net.URLEncoder
import java.security.MessageDigest
import java.util.Locale
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.Executors
import java.util.concurrent.Future

/**
 * 后台听书：网页把一段段句子交给这里，这里负责合成（书库的微软自然语音，或手机自带语音）、
 * 播放、锁屏和通知栏控制、耳机按键、音频焦点、睡眠定时。Wear OS 手表会自动显示这个播放会话。
 * 网页不在了（App 被划掉、熄屏后网页被系统回收或冻结）、或者要下一章时网页半天不回应：
 * 这里直接从书库按章取正文（和手表听书一样，/api/books/{id}/chapters），切成句子接着读，进度照样存。
 */
class NarrationService : Service() {
    /** 一句话；新版网页还带着它在书里的位置（loc 和进度里的 location 一样，pct 是 0～1 的百分比） */
    data class Seg(val id: String, val text: String, val loc: String = "", val pct: Double = -1.0, val voice: String = "")

    private val handler = Handler(Looper.getMainLooper())
    private val pool = Executors.newFixedThreadPool(2)
    private val app get() = ShukuApp.app

    private lateinit var session: MediaSession
    private lateinit var audio: AudioManager
    private var focusRequest: AudioFocusRequest? = null
    private var wifiLock: WifiManager.WifiLock? = null
    private var wakeLock: PowerManager.WakeLock? = null

    private val queue = ArrayList<Seg>()
    private var index = 0
    private var playing = false
    private var waitingMore = false
    private var needSent = false
    private var ended = false
    private var status = ""
    private var book = JSONObject()
    private var progressBase = -1L // 书库进度的版本（网页给的），-1：旧版网页没给，不带
    private var cover: Bitmap? = null
    private var voice = ""
    private var lang = ""            // 书的语言（zh、en、th…）：手机自带语音读拉丁字母时按它选
    private val voices = HashMap<String, String>() // 每种语言用哪个声音（网页给的）：从书库取的句子按文字挑
    private var speed = 1.0
    private var timerMin = 0
    private var useDevice = false
    private var pausedByFocus = false
    private var stopped = false

    // 网页不在时由这里记进度
    private var webGen = -1          // 发来这本书的网页（Events.generation）
    private var orphan = false       // 网页已经不在了
    private var fileId = 0
    private var posSeg: Seg? = null  // 读到的、还没存的位置
    private var lastSave = 0L        // 上次存进度的时间（elapsedRealtime）
    private var lastTick = 0L        // 听书时长从这个时间开始算（0 表示没在播放）
    private var listenMs = 0L        // 网页不在时听书的时长，存进度时一起报给书库

    private var player: MediaPlayer? = null
    private var playerSeg: String? = null
    private val audioJobs = HashMap<String, Future<File>>()
    // 网页不回应时从书库取章节接着读
    private var feed = false          // 已经改由这里取句子
    private var feedBusy = false      // 正在取下一章
    private var feedChapters = JSONArray()
    private var feedFile = 0L         // 章节是按哪个文件切的
    private var feedNext = -1         // 下一个要取的章节
    private var feedGen = 0           // 换书、重新开始时作废还在取的
    private val needWatch = Runnable { if (needSent && playing && !ended && !feed) startFeed("网页没有回应") }

    // 合成失败（熄屏后断网、网络受限）：先等一等再试，不马上改用手机语音、更不暂停
    private var netFails = 0
    private var deviceBroken = false  // 手机没有可用的语音引擎（墨水屏常见）：只能等网络
    // 一句合成了 8 秒还没好：在通知栏、锁屏上说明（多半是熄屏后手机限制了联网）
    private val slowTask = Runnable { if (playing && status == "合成中…") { status = "等待网络…"; publish() } }
    private val retryTask = Runnable { if (playing) { synchronized(audioJobs) { queue.getOrNull(index)?.let { audioJobs.remove(it.id) } }; playCurrent() } }

    private var tts: TextToSpeech? = null
    private var ttsReady = false
    private var utterance: String? = null

    // ---------- 生命周期 ----------

    override fun onCreate() {
        super.onCreate()
        instance = this
        audio = getSystemService(AUDIO_SERVICE) as AudioManager
        session = MediaSession(this, "shuku-narration")
        session.setCallback(object : MediaSession.Callback() {
            override fun onPlay() = resume()
            override fun onPause() = pause()
            override fun onSkipToNext() = step(1)
            override fun onSkipToPrevious() = step(-1)
            override fun onStop() = stop("")
        })
        session.setSessionActivity(PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE))
        // startForegroundService 之后 5 秒内必须调用 startForeground
        goForeground()
        registerReceiver(noisy, IntentFilter(AudioManager.ACTION_AUDIO_BECOMING_NOISY))
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACT_PLAY -> resume()
            ACT_PAUSE -> pause()
            ACT_NEXT -> step(1)
            ACT_PREV -> step(-1)
            ACT_STOP -> stop("")
        }
        while (true) {
            val (c, a) = pending.poll() ?: break
            handle(c, a)
        }
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        if (instance === this) instance = null
        if (!stopped) {
            tick()
            flushPosition()
        }
        try { unregisterReceiver(noisy) } catch (e: Exception) {}
        releasePlayer()
        tts?.shutdown()
        session.release()
        releaseLocks()
        pool.shutdownNow()
        super.onDestroy()
    }

    private val noisy = object : BroadcastReceiver() {
        override fun onReceive(c: Context?, i: Intent?) = pause() // 拔掉耳机就暂停
    }

    // ---------- 网页发来的命令 ----------

    fun handle(cmd: String, arg: String) {
        when (cmd) {
            "start" -> {
                val j = try { JSONObject(arg) } catch (e: Exception) { return }
                // 上一本书在网页不在时读到的位置先存好
                tick()
                flushPosition()
                book = j.optJSONObject("book") ?: JSONObject()
                fileId = book.optInt("file_id")
                progressBase = if (j.has("progressBase") && !j.isNull("progressBase")) j.optLong("progressBase", -1) else -1
                webGen = Events.generation
                orphan = false
                posSeg = null
                listenMs = 0
                lastSave = SystemClock.elapsedRealtime()
                voice = j.optString("voice")
                lang = j.optString("lang")
                voices.clear()
                j.optJSONObject("voices")?.let { o -> o.keys().forEach { k -> o.optString(k).takeIf { it.isNotEmpty() }?.let { voices[k] = it } } }
                speed = j.optDouble("speed", 1.0)
                feed = false
                feedBusy = false
                feedNext = -1
                feedGen++
                handler.removeCallbacks(needWatch)
                useDevice = app.config.str("tts_engine", "server") == "device" || !app.link.online
                releasePlayer()
                tts?.stop()
                synchronized(audioJobs) { audioJobs.values.forEach { it.cancel(true) }; audioJobs.clear() }
                queue.clear()
                add(j.optJSONArray("segments"))
                index = 0
                ended = false
                needSent = false
                waitingMore = false
                loadCover()
                session.isActive = true
                playing = true
                lastTick = SystemClock.elapsedRealtime()
                requestFocus()
                playCurrent()
            }
            "append" -> {
                if (feed) return // 已经改由这里从书库取了，网页晚到的句子不要（会重复）
                handler.removeCallbacks(needWatch)
                add(try { JSONObject(arg).optJSONArray("segments") } catch (e: Exception) { null })
                needSent = false
                if (waitingMore && playing) { waitingMore = false; index = minOf(index, queue.size - 1); playCurrent() }
            }
            "end" -> {
                if (feed) return
                handler.removeCallbacks(needWatch)
                ended = true
                if (waitingMore) stop(if (timerMin == -1) "本章读完了" else "全书读完了")
            }
            "pause" -> pause()
            "resume" -> resume()
            "next" -> step(1)
            "prev" -> step(-1)
            "stop" -> stop("")
            "speed" -> {
                speed = arg.toDoubleOrNull() ?: speed
                restartCurrent()
            }
            "voice" -> {
                // 换的是哪种语言的声音：这种语言的句子都换成它
                val code = voiceCode(arg)
                if (voices.isEmpty() || voiceCode(voice) == code) voice = arg
                if (code.isNotEmpty() && voices.isNotEmpty()) voices[code] = arg
                for (k in queue.indices) if (queue[k].voice.isNotEmpty() && voiceCode(queue[k].voice) == code) queue[k] = queue[k].copy(voice = arg)
                restartCurrent()
            }
            "timer" -> setTimer(arg.toIntOrNull() ?: 0)
        }
        publish()
    }

    private fun add(arr: JSONArray?) {
        if (arr == null) return
        for (i in 0 until arr.length()) {
            val o = arr.optJSONObject(i) ?: continue
            val id = o.optString("id")
            val text = o.optString("text")
            if (id.isEmpty() || text.isBlank()) continue
            // 旧版网页没有 loc、pct
            queue += Seg(id, text, o.optString("loc"), if (o.has("pct")) o.optDouble("pct", -1.0) else -1.0, o.optString("voice"))
        }
    }

    // ---------- 播放 ----------

    private fun playCurrent() {
        if (!playing) return
        val seg = queue.getOrNull(index)
        if (seg == null) {
            when {
                ended -> stop(if (timerMin == -1) "本章读完了" else "全书读完了")
                // 网页没了：从书库取后面的章节接着读
                !webAlive() || feed -> { waitingMore = true; status = "加载下一章…"; publish(); startFeed("网页不在了") }
                else -> { waitingMore = true; status = "加载下一章…"; publish(); askMore() }
            }
            return
        }
        // 从书库取的句子网页不认识 id：带上位置，网页回来时翻到这里
        val ev = JSONObject().put("type", "seg").put("id", seg.id)
        if (feed && seg.loc.isNotEmpty()) ev.put("loc", seg.loc).put("pct", seg.pct)
        Events.emit("tts", ev)
        tick()
        // 一直由这里记进度（最多 30 秒存一次，暂停、停止时马上存）：关屏后网页被冻结，
        // 网页自己存不了，以前只在网页不在了才由这里存，关屏听的那一段就丢了
        notePosition(seg)
        askMore()
        if (useDevice) return speakDevice(seg)
        val job = audioFor(seg)
        if (!job.isDone) { status = "合成中…"; publish(); handler.removeCallbacks(slowTask); handler.postDelayed(slowTask, 8000) }
        prefetch()
        pool.execute {
            val file = try { job.get() } catch (e: Exception) { null }
            handler.post {
                if (queue.getOrNull(index)?.id != seg.id || !playing) return@post
                handler.removeCallbacks(slowTask)
                if (file == null) synthFailed() else { netFails = 0; startPlayer(seg, file) }
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
            p.setOnCompletionListener { advance() }
            p.setOnErrorListener { _, _, _ -> fallback("音频无法播放"); true }
            p.prepare()
            p.start()
            player = p
            playerSeg = seg.id
            status = ""
            publish()
        } catch (e: Exception) {
            fallback("音频无法播放")
        }
    }

    private fun audioFor(seg: Seg): Future<File> = synchronized(audioJobs) {
        audioJobs.getOrPut(seg.id) {
            val v = seg.voice.ifEmpty { voice }
            val s = speed
            pool.submit<File> {
                val key = sha1("$v|$s|${seg.text}")
                val f = File(cacheDir, "tts/$key.mp3")
                if (f.exists() && f.length() > 0) return@submit f
                val body = JSONObject().put("text", seg.text).put("voice", v).put("lang", lang).put("speed", s)
                val r = Http.postJson(app.link.url("/api/tts"), app.config.token, body, timeoutMs = 60000)
                if (!r.ok) throw IOException(r.errorMessage())
                f.parentFile?.mkdirs()
                f.writeBytes(r.body)
                f
            }
        }
    }

    // 预先合成后面三十句（三五分钟）：句子之间不停顿；有的手机熄屏后限制后台联网，手上有存货就接得上，亮屏时再补
    private fun prefetch() {
        for (k in 1..PREFETCH) queue.getOrNull(index + k)?.let { audioFor(it) }
        synchronized(audioJobs) {
            val keep = (index - 2..index + PREFETCH + 1).mapNotNull { queue.getOrNull(it)?.id }.toSet()
            audioJobs.keys.filter { it !in keep }.forEach { audioJobs.remove(it)?.cancel(false) }
        }
    }

    /** 快读完手上的句子时向网页要下一段。网页没了（App 被划掉、网页重建过）就不要了，读完手上的就停 */
    private fun askMore() {
        if (ended || queue.size - index > PREFETCH + 6) return
        if (feed) { feedMore(); return }
        if (needSent || !webAlive()) return
        needSent = true
        Events.emit("tts", JSONObject().put("type", "need"))
        // 熄屏久了网页可能被系统冻结：15 秒还没给下一段，就自己从书库取
        handler.removeCallbacks(needWatch)
        handler.postDelayed(needWatch, 15_000)
    }

    // ---------- 网页不在或不回应时：从书库按章取正文 ----------

    /** 改由这里取句子：先问书库刚读到的那句在第几章，从那句后面接着读 */
    private fun startFeed(@Suppress("UNUSED_PARAMETER") why: String) {
        if (feed) { feedMore(); return }
        val id = book.optInt("id")
        if (id <= 0 || timerMin == -1) { stop(if (timerMin == -1) "本章读完了" else webGoneMessage()); return }
        feed = true
        needSent = false
        handler.removeCallbacks(needWatch)
        val last = queue.lastOrNull { it.loc.isNotEmpty() }
        val lastText = queue.lastOrNull()?.text ?: ""
        val gen = feedGen
        feedBusy = true
        pool.execute {
            val r = runCatching {
                var q = "/api/books/$id/chapters?file=$fileId"
                if (last != null) q += "&loc=" + URLEncoder.encode(last.loc, "UTF-8") + "&pct=" + last.pct
                val toc = Http.getJson(app.link.url(q), app.config.token, 20000)
                val chs = toc.optJSONArray("chapters") ?: JSONArray()
                val start = toc.optJSONObject("resume")?.optInt("i") ?: 0
                val frac = toc.optJSONObject("resume")?.optDouble("frac", 0.0) ?: 0.0
                var segs = chapterSegs(id, toc.optLong("file_id"), chs, start, lastText, frac)
                var next = start + 1
                // 刚好读到这一章末尾：从下一章开始
                while (segs.isEmpty() && next < chs.length()) { segs = chapterSegs(id, toc.optLong("file_id"), chs, next, "", 0.0); next++ }
                Pair(Triple(chs, toc.optLong("file_id"), next), segs)
            }
            handler.post {
                if (gen != feedGen || stopped) return@post
                feedBusy = false
                val res = r.getOrNull()
                if (res == null) {
                    // 书库也连不上：读完手上的就停
                    feed = false
                    if (waitingMore) stop("连不上书库，听书停了：" + (r.exceptionOrNull()?.message ?: ""))
                    return@post
                }
                val (meta, segs) = res
                feedChapters = meta.first
                feedFile = meta.second
                feedNext = meta.third
                appendFeed(segs)
            }
        }
    }

    /** 快读完了：取下一章 */
    private fun feedMore() {
        if (feedBusy || ended || queue.size - index > PREFETCH + 6) return
        if (feedNext < 0 || feedNext >= feedChapters.length()) { ended = true; if (waitingMore) stop("全书读完了"); return }
        val id = book.optInt("id")
        val i = feedNext
        val gen = feedGen
        feedBusy = true
        pool.execute {
            val r = runCatching { chapterSegs(id, feedFile, feedChapters, i, "", 0.0) }
            handler.post {
                if (gen != feedGen || stopped) return@post
                feedBusy = false
                val segs = r.getOrNull()
                if (segs == null) {
                    // 取不到（断网）：一会儿再试；手上的读完了就停
                    if (waitingMore) stop("连不上书库，听书停了")
                    else handler.postDelayed({ if (gen == feedGen) feedMore() }, 20_000)
                    return@post
                }
                feedNext = i + 1
                appendFeed(segs)
            }
        }
    }

    private fun appendFeed(segs: List<Seg>) {
        queue += segs
        if (feedNext >= feedChapters.length() && segs.isEmpty()) ended = true
        if (waitingMore && playing) {
            waitingMore = false
            if (index < queue.size) playCurrent() else feedMore()
        } else if (segs.isEmpty()) feedMore()
        publish()
    }

    /**
     * 第 i 章切成句子，带上位置（sec: / p:，和阅读器、手表一样）。after 是刚读完的那句：在这一章里找到它，
     * 从它后面开始；找不到时从 frac（这一章里的比例）开始
     */
    private fun chapterSegs(id: Int, fid: Long, chs: JSONArray, i: Int, after: String, frac: Double): List<Seg> {
        if (i < 0 || i >= chs.length()) return emptyList()
        val ch = Http.getJson(app.link.url("/api/books/$id/chapters/$i?file=$fid"), app.config.token, 20000)
        val text = BookText.format(ch.optString("text"))
        val len = maxOf(1, text.length)
        val all = BookText.sentences(text)
        var from = (frac.coerceIn(0.0, 1.0) * len).toInt()
        val key = after.filterNot { it.isWhitespace() }.take(16)
        if (key.length >= 4) {
            // 正文里的空白和网页切出来的不一定一样：去掉空白再找
            val sb = StringBuilder(); val map = ArrayList<Int>()
            text.forEachIndexed { k, c -> if (!c.isWhitespace()) { sb.append(c); map += k } }
            // 同一句话可能出现好几次：用离 frac 最近的那一处
            val near = from
            var at = -1
            var k = sb.indexOf(key)
            while (k >= 0) {
                if (at < 0 || kotlin.math.abs(map[k] - near) < kotlin.math.abs(map[at] - near)) at = k
                k = sb.indexOf(key, k + 1)
            }
            if (at >= 0) {
                val restKey = after.filterNot { it.isWhitespace() }
                val endAt = map.getOrNull(minOf(map.size - 1, at + restKey.length - 1)) ?: map[at]
                from = endAt + 1
            }
        }
        return all.filter { it.start >= from - 1 || (from == 0) }.mapIndexed { n, s ->
            val f = s.start.toDouble() / len
            Seg("srv:$i:$n", s.text, BookPos.location(chs, i, f), BookPos.percent(ch, f, false), voiceForText(s.text))
        }
    }

    private fun advance() {
        if (!playing) return
        index++
        playCurrent()
    }

    private fun step(d: Int) {
        if (queue.isEmpty()) return
        index = (index + d).coerceIn(0, queue.size - 1)
        playing = true
        releasePlayer()
        tts?.stop()
        requestFocus()
        playCurrent()
        publish()
    }

    private fun restartCurrent() {
        synchronized(audioJobs) { audioJobs.values.forEach { it.cancel(true) }; audioJobs.clear() }
        if (playing) { releasePlayer(); tts?.stop(); playCurrent() }
    }

    private fun pause() {
        if (!playing) return
        tick()
        playing = false
        lastTick = 0
        player?.takeIf { it.isPlaying }?.pause()
        tts?.stop()
        releaseLocks()
        flushPosition()
        publish()
    }

    private fun resume() {
        if (playing || queue.isEmpty()) return
        playing = true
        lastTick = SystemClock.elapsedRealtime()
        requestFocus()
        val p = player
        if (p != null && playerSeg == queue.getOrNull(index)?.id && !useDevice) {
            p.start()
            acquireLocks()
        } else playCurrent()
        publish()
    }

    private fun stop(reason: String) {
        if (stopped) return
        tick()
        flushPosition()
        stopped = true
        // 马上让出 instance：紧接着再点“听书”会启动一个新的服务，而不是交给这个正在退出的
        if (instance === this) instance = null
        playing = false
        releasePlayer()
        tts?.stop()
        feedGen++
        handler.removeCallbacks(needWatch)
        handler.removeCallbacks(retryTask)
        handler.removeCallbacks(timerTask)
        timerMin = 0
        session.isActive = false
        focusRequest?.let { audio.abandonAudioFocusRequest(it) }
        releaseLocks()
        Events.emit("tts", JSONObject().put("type", "stopped").put("reason", reason))
        // 网页不在了，没人显示停下的原因：留一条通知
        if (reason.isNotEmpty() && !webAlive()) notifyStopped(reason)
        WearSync.pushState(this, JSONObject().put("active", false), force = true)
        File(cacheDir, "tts").listFiles()?.forEach { it.delete() }
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun releasePlayer() {
        player?.let { try { it.stop() } catch (e: Exception) {}; it.release() }
        player = null
        playerSeg = null
    }

    /**
     * 这一句没合成出来：多半是熄屏后网络断了一下（墨水屏关屏会关 WiFi、手机省电限制后台联网）。
     * 先重新找一下书库的地址（内网 / Tailscale），隔几秒再试；试了一分半钟还不行才改用手机自带语音。
     * 手机没有语音引擎时一直等网络（每 30 秒试一次），不暂停
     */
    private fun synthFailed() {
        netFails++
        if (netFails > RETRIES.size && !deviceBroken) { netFails = 0; fallback("连不上书库"); return }
        // 等了十来分钟还是连不上：暂停（省电），网络好了点播放接着读
        if (netFails > 25) { netFails = 0; pause(); status = "连不上书库，已暂停"; publish(); return }
        status = "等待网络…"
        publish()
        acquireLocks()
        val delay = RETRIES.getOrElse(netFails - 1) { 30_000L }
        Thread { try { app.link.probe() } catch (e: Exception) {} }.start()
        handler.removeCallbacks(retryTask)
        handler.postDelayed(retryTask, delay)
    }

    /** 书库语音不可用（NAS 连不上微软、断网）时改用手机自带语音 */
    private fun fallback(why: String) {
        if (deviceBroken) { synthFailed(); return }
        if (useDevice) { pause(); return }
        useDevice = true
        Events.emit("tts", JSONObject().put("type", "error").put("message", "$why，已改用手机自带语音"))
        playCurrent()
    }

    // ---------- 网页不在时（App 被划掉、网页崩溃后重建）：读完手上的句子就停，进度由这里存 ----------

    /** 发来这本书句子的那个网页还在 */
    private fun webAlive() = Events.attached && Events.generation == webGen

    private fun webGoneMessage() = if (Events.attached) "网页重新打开过，回到书里点“听书”可以接着听" else "书库 App 已关闭，打开 App 可以接着听"

    /** 网页换了或者没了（Events 通知过来，在主线程） */
    private fun checkWeb() {
        if (stopped || webAlive()) return
        if (!orphan) {
            orphan = true
            // 在这之前的时长网页已经算过了
            lastTick = if (playing) SystemClock.elapsedRealtime() else 0
        }
        // 从现在起由这里记进度：先把正在读的这句存下来，App 重新打开时书就停在这里
        queue.getOrNull(index)?.let { notePosition(it, now = true) }
        // 正在等网页给下一章：不会有了，从书库取
        if (waitingMore || needSent) startFeed("网页不在了")
    }

    /** 记下读到哪一句；最多 30 秒存一次（暂停、停止时也会存） */
    private fun notePosition(seg: Seg, now: Boolean = false) {
        if (seg.loc.isEmpty() || seg.pct !in 0.0..1.0) return // 旧版网页没有位置
        posSeg = seg
        if (now || SystemClock.elapsedRealtime() - lastSave >= 30_000) flushPosition()
    }

    /** 把记下的位置和这段时间的听书时长交给 ProgressSync（存在手机上，联网时传给书库） */
    private fun flushPosition() {
        val seg = posSeg ?: return
        posSeg = null
        lastSave = SystemClock.elapsedRealtime()
        val id = book.optInt("id")
        if (id <= 0 || fileId <= 0) return
        val secs = (listenMs / 1000).toInt().coerceIn(0, ProgressSync.MAX_SECONDS)
        listenMs -= secs * 1000L
        val body = JSONObject().put("file_id", fileId).put("location", seg.loc).put("percent", seg.pct)
            .put("device", MainActivity.deviceName()).put("seconds", secs)
        // 带上书库进度的版本：暂停期间别的设备读到了后面，这次不覆盖（书库回冲突，网页提示跳过去）；
        // 接着听下去就以这台为准（见 onProgress），和网页上的做法一样
        if (progressBase >= 0) body.put("base", progressBase)
        try { app.progress.save(id, body.toString()) } catch (e: Exception) {}
    }

    /** 听书时长：网页在的时候网页自己算阅读时长，这里只算网页不在的时候 */
    private fun tick() {
        val now = SystemClock.elapsedRealtime()
        if (lastTick > 0 && (!webAlive() || feed)) listenMs += (now - lastTick).coerceIn(0, 120_000)
        lastTick = if (playing) now else 0
    }

    private fun notifyStopped(reason: String) {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(this, ShukuApp.CH_PLAY)
            .setSmallIcon(android.R.drawable.ic_lock_silent_mode_off)
            .setContentTitle(book.optString("title").ifEmpty { "书库 · 听书" })
            .setContentText(reason)
            .setStyle(Notification.BigTextStyle().bigText(reason))
            .setContentIntent(open)
            .setAutoCancel(true)
            .build()
        try { getSystemService(NotificationManager::class.java)?.notify(NOTIF_STOPPED, n) } catch (e: Exception) {}
    }

    // ---------- 手机自带语音 ----------

    private fun speakDevice(seg: Seg) {
        val t = tts
        if (t == null || !ttsReady) {
            status = "准备语音…"
            publish()
            if (t == null) tts = TextToSpeech(this) { st ->
                handler.post {
                    ttsReady = st == TextToSpeech.SUCCESS
                    if (!ttsReady) {
                        tts = null
                        if (app.config.str("tts_engine", "server") != "device") {
                            // 是因为连不上书库才换过来的：没有手机语音就等网络，接着用书库语音
                            deviceBroken = true
                            useDevice = false
                            synthFailed()
                        } else {
                            Events.emit("tts", JSONObject().put("type", "error").put("message", "手机没有可用的语音引擎，请在系统设置里安装“Google 文字转语音”等"))
                            pause()
                        }
                    } else {
                        tts?.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
                            override fun onStart(id: String?) {}
                            override fun onDone(id: String?) { handler.post { if (id == utterance) advance() } }
                            @Deprecated("Deprecated in Java")
                            override fun onError(id: String?) { handler.post { if (id == utterance) advance() } }
                        })
                        queue.getOrNull(index)?.let { speakDevice(it) }
                    }
                }
            }
            return
        }
        val loc = localeFor(seg.text)
        val picked = if (voice.isNotEmpty()) t.voices?.firstOrNull { it.name == voice && it.locale.language == loc.language } else null
        if (picked != null) t.voice = picked else if (t.voice?.locale?.language != loc.language) t.language = loc
        t.setSpeechRate(speed.toFloat())
        utterance = seg.id
        acquireLocks()
        t.speak(seg.text, TextToSpeech.QUEUE_FLUSH, Bundle(), seg.id)
        status = ""
        publish()
    }

    /** 声音的语言：zh-CN-XiaoxiaoNeural → zh（自定义接口的声音名没有语言，返回空） */
    private fun voiceCode(v: String): String = if (Regex("^[a-z]{2,3}-[A-Z]{2}").containsMatchIn(v)) v.substringBefore('-') else ""

    /**
     * 一句用哪个声音（从书库取的句子；网页给的句子自己带着）：看这句主要是什么文字（汉字、假名算 4 个字母，和书库、网页一样），
     * 拉丁字母为主时按书的语言（不是拉丁字母的语言就用英语）
     */
    private fun voiceForText(text: String): String {
        if (voices.isEmpty()) return ""
        var latin = 0; var thai = 0; var hangul = 0; var kana = 0; var han = 0; var cyr = 0; var arabic = 0
        for (c in text) {
            when (Character.UnicodeScript.of(c.code)) {
                Character.UnicodeScript.LATIN -> latin++
                Character.UnicodeScript.THAI -> thai++
                Character.UnicodeScript.HANGUL -> hangul += 2
                Character.UnicodeScript.HIRAGANA, Character.UnicodeScript.KATAKANA -> kana += 4
                Character.UnicodeScript.HAN -> han += 4
                Character.UnicodeScript.CYRILLIC -> cyr++
                Character.UnicodeScript.ARABIC -> arabic++
                else -> {}
            }
        }
        val total = latin + thai + hangul + kana + han + cyr + arabic
        if (total == 0) return ""
        val nonLatin = setOf("zh", "ja", "ko", "th", "ru", "uk", "bg", "ar", "fa", "he", "hi", "el")
        val code = when {
            kana > 0 && kana + han >= total / 2 -> "ja"
            else -> listOf("latin" to latin, "th" to thai, "ko" to hangul, "ja" to kana, "zh" to han, "ru" to cyr, "ar" to arabic).maxByOrNull { it.second }!!.first
        }
        val c = if (code == "latin") (if (lang.isNotEmpty() && lang !in nonLatin) lang else "en") else code
        return voices[c] ?: ""
    }

    /** 手机自带语音用哪种语言读：看文字种类（泰、韩、日、中、俄、阿拉伯）；拉丁字母按书的语言，中文书里的就用英语 */
    private fun localeFor(text: String): Locale {
        var thai = 0; var hangul = 0; var kana = 0; var han = 0; var cyr = 0; var arabic = 0
        for (c in text) {
            when (Character.UnicodeScript.of(c.code)) {
                Character.UnicodeScript.THAI -> thai++
                Character.UnicodeScript.HANGUL -> hangul++
                Character.UnicodeScript.HIRAGANA, Character.UnicodeScript.KATAKANA -> kana++
                Character.UnicodeScript.HAN -> han++
                Character.UnicodeScript.CYRILLIC -> cyr++
                Character.UnicodeScript.ARABIC -> arabic++
                else -> {}
            }
        }
        return when {
            kana > 0 -> Locale.JAPANESE
            thai > 0 && thai >= han * 4 -> Locale("th", "TH")
            hangul > 0 && hangul * 2 >= han * 4 -> Locale.KOREAN
            han > 0 -> Locale.SIMPLIFIED_CHINESE
            cyr > 0 -> Locale("ru", "RU")
            arabic > 0 -> Locale("ar")
            lang.isNotEmpty() && lang !in setOf("zh", "ja", "ko", "th", "ru", "ar") -> Locale(lang)
            else -> Locale.US
        }
    }

    // ---------- 定时 ----------

    private val timerTask = Runnable {
        if (playing) pause()
        timerMin = 0
        status = "定时结束"
        publish()
    }

    private fun setTimer(min: Int) {
        handler.removeCallbacks(timerTask)
        timerMin = min
        if (min > 0) handler.postDelayed(timerTask, min * 60_000L)
    }

    // ---------- 音频焦点、锁 ----------

    private fun requestFocus() {
        val req = focusRequest ?: AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
            .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
            .setOnAudioFocusChangeListener({ change ->
                when (change) {
                    AudioManager.AUDIOFOCUS_LOSS -> pause()
                    AudioManager.AUDIOFOCUS_LOSS_TRANSIENT, AudioManager.AUDIOFOCUS_LOSS_TRANSIENT_CAN_DUCK -> if (playing) { pausedByFocus = true; pause() }
                    AudioManager.AUDIOFOCUS_GAIN -> if (pausedByFocus) { pausedByFocus = false; resume() }
                }
            }, handler).build().also { focusRequest = it }
        audio.requestAudioFocus(req)
        acquireLocks()
    }

    @Suppress("DEPRECATION")
    private fun acquireLocks() {
        if (wifiLock == null) {
            val wm = applicationContext.getSystemService(WIFI_SERVICE) as WifiManager
            wifiLock = wm.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "shuku:narration").apply { setReferenceCounted(false) }
        }
        if (wakeLock == null) {
            wakeLock = (getSystemService(POWER_SERVICE) as PowerManager).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "shuku:narration").apply { setReferenceCounted(false) }
        }
        try { wifiLock?.acquire() } catch (e: Exception) {}
        wakeLock?.acquire(3 * 3600_000L)
    }

    private fun releaseLocks() {
        try { wifiLock?.takeIf { it.isHeld }?.release() } catch (e: Exception) {}
        try { wakeLock?.takeIf { it.isHeld }?.release() } catch (e: Exception) {}
    }

    // ---------- 状态：网页、锁屏、通知栏、手表 ----------

    private fun stateJson(): JSONObject = JSONObject().put("active", true).put("playing", playing).put("speed", speed).put("timer", timerMin)
        .put("status", status).put("title", book.optString("title")).put("author", book.optString("author"))
        .put("text", queue.getOrNull(index)?.text ?: "")

    // 锁屏的媒体控制、手表只在播放/暂停、换书、换封面、定时、语速、出错这些变化时更新。
    // 以前每读一句都更新一次：手机的媒体会话会同步到手表（Wear OS 的媒体控制），再加上发给书库手表版的状态，
    // 手表每隔几秒就被叫醒一次，很费电。手表要看当前读到哪一句时自己来要（打开首页时）
    private var lastShown = ""
    private var lastNotifyAt = 0L

    private fun publish() {
        if (stopped) return // 停止以后不要再把通知和手表状态带回来
        Events.emit("tts", stateJson().put("type", "state")) // 给网页（手机本机），每句都发
        val shownStatus = if (status == "合成中…" || status == "加载下一章…") "" else status
        val sig = listOf(playing, book.optString("title"), book.optString("author"), System.identityHashCode(cover), timerMin, speed, shownStatus).joinToString("|")
        val now = SystemClock.elapsedRealtime()
        if (sig == lastShown) {
            // 只是换了一句：通知栏里显示的句子最多 15 秒更新一次（这条通知只在手机上显示，不转到手表）
            if (now - lastNotifyAt >= 15_000) {
                lastNotifyAt = now
                getSystemService(android.app.NotificationManager::class.java)?.notify(NOTIF_ID, notification())
            }
            return
        }
        lastShown = sig
        lastNotifyAt = now
        val actions = PlaybackState.ACTION_PLAY or PlaybackState.ACTION_PAUSE or PlaybackState.ACTION_PLAY_PAUSE or
            PlaybackState.ACTION_SKIP_TO_NEXT or PlaybackState.ACTION_SKIP_TO_PREVIOUS or PlaybackState.ACTION_STOP
        session.setPlaybackState(PlaybackState.Builder().setActions(actions)
            .setState(if (playing) PlaybackState.STATE_PLAYING else PlaybackState.STATE_PAUSED, PlaybackState.PLAYBACK_POSITION_UNKNOWN, if (playing) 1f else 0f).build())
        session.setMetadata(MediaMetadata.Builder()
            .putString(MediaMetadata.METADATA_KEY_TITLE, book.optString("title"))
            .putString(MediaMetadata.METADATA_KEY_ARTIST, book.optString("author"))
            .putString(MediaMetadata.METADATA_KEY_ALBUM, "书库 · 听书")
            .putString(MediaMetadata.METADATA_KEY_DISPLAY_SUBTITLE, shownStatus.ifEmpty { book.optString("author") })
            .apply { cover?.let { putBitmap(MediaMetadata.METADATA_KEY_ALBUM_ART, it) } }
            .build())
        getSystemService(android.app.NotificationManager::class.java)?.notify(NOTIF_ID, notification())
        WearSync.pushState(this, stateJson(), force = true)
    }

    private fun goForeground() {
        val n = notification()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) startForeground(NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK)
        else startForeground(NOTIF_ID, n)
    }

    private fun action(icon: Int, title: String, act: String, code: Int) = Notification.Action.Builder(
        Icon.createWithResource(this, icon), title,
        PendingIntent.getService(this, code, Intent(this, NarrationService::class.java).setAction(act), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)).build()

    private fun notification(): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, ShukuApp.CH_PLAY)
            .setSmallIcon(android.R.drawable.ic_lock_silent_mode_off)
            .setContentTitle(book.optString("title").ifEmpty { "书库 · 听书" })
            .setContentText(status.ifEmpty { queue.getOrNull(index)?.text ?: "准备中…" })
            .setLargeIcon(cover)
            .setContentIntent(open)
            .setDeleteIntent(PendingIntent.getService(this, 9, Intent(this, NarrationService::class.java).setAction(ACT_STOP), PendingIntent.FLAG_IMMUTABLE))
            .setOngoing(playing)
            .setVisibility(Notification.VISIBILITY_PUBLIC)
            .setCategory(Notification.CATEGORY_TRANSPORT)
            .setOnlyAlertOnce(true)
            .setLocalOnly(true) // 不转到手表：手表上有系统的媒体控制和书库手表版，转过去只会频繁叫醒手表
            .addAction(action(android.R.drawable.ic_media_previous, "上一句", ACT_PREV, 1))
            .addAction(if (playing) action(android.R.drawable.ic_media_pause, "暂停", ACT_PAUSE, 2) else action(android.R.drawable.ic_media_play, "继续", ACT_PLAY, 3))
            .addAction(action(android.R.drawable.ic_media_next, "下一句", ACT_NEXT, 4))
            .addAction(action(android.R.drawable.ic_menu_close_clear_cancel, "停止", ACT_STOP, 5))
            .setStyle(Notification.MediaStyle().setMediaSession(session.sessionToken).setShowActionsInCompactView(0, 1, 2))
            .build()
    }

    private fun loadCover() {
        cover = null
        val url = book.optString("cover")
        if (url.isEmpty()) return
        pool.execute {
            try {
                val r = Http.request(url, token = app.config.token, timeoutMs = 10000)
                if (r.ok) BitmapFactory.decodeByteArray(r.body, 0, r.body.size)?.let { bmp -> handler.post { cover = bmp; publish() } }
            } catch (e: Exception) {}
        }
    }

    /** 书库收到了这本书的进度（ProgressSync 转过来）：记下新的版本 */
    private fun onProgress(id: Int, r: JSONObject) {
        if (id != book.optInt("id") || progressBase < 0) return
        // 冲突：别的设备读到了别处，这次没覆盖；还接着听的话，下一次就以这台为准
        val v = r.optLong("updated_at")
        if (v > progressBase) progressBase = v
    }

    companion object {
        @Volatile var instance: NarrationService? = null
            private set

        /** ProgressSync 传完进度后调用（任何线程） */
        fun progressSaved(id: Int, r: JSONObject) {
            val s = instance ?: return
            s.handler.post { s.onProgress(id, r) }
        }
        private val pending = ConcurrentLinkedQueue<Pair<String, String>>()
        private const val PREFETCH = 30
        private val RETRIES = listOf(3_000L, 5_000L, 10_000L, 20_000L, 30_000L, 30_000L)
        private const val NOTIF_ID = 7
        private const val NOTIF_STOPPED = 8
        const val ACT_PLAY = "app.shuku.PLAY"
        const val ACT_PAUSE = "app.shuku.PAUSE"
        const val ACT_NEXT = "app.shuku.NEXT"
        const val ACT_PREV = "app.shuku.PREV"
        const val ACT_STOP = "app.shuku.STOP"

        /** 网页（ShukuNative.tts）、手表都从这里发命令 */
        fun command(ctx: Context, cmd: String, arg: String) {
            val s = instance
            if (s != null) {
                s.handler.post { s.handle(cmd, arg) }
                return
            }
            if (cmd != "start") return
            pending.add(cmd to arg)
            ctx.startForegroundService(Intent(ctx, NarrationService::class.java))
        }

        fun state(): JSONObject = instance?.stateJson() ?: JSONObject().put("active", false)

        /** 网页换了、没了（Events 调用，任何线程） */
        fun webChanged() {
            val s = instance ?: return
            s.handler.post { s.checkWeb() }
        }

        private fun sha1(s: String): String = MessageDigest.getInstance("SHA-1").digest(s.toByteArray()).joinToString("") { "%02x".format(it) }
    }
}
