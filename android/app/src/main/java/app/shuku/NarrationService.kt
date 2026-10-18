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
import app.shuku.core.Http
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.security.MessageDigest
import java.util.Locale
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.Executors
import java.util.concurrent.Future

/**
 * 后台听书：网页把一段段句子交给这里，这里负责合成（书库的微软自然语音，或手机自带语音）、
 * 播放、锁屏和通知栏控制、耳机按键、音频焦点、睡眠定时。Wear OS 手表会自动显示这个播放会话。
 * App 被划掉、网页崩溃重建以后，给句子的网页就没了：读完手上的句子就停，期间由这里存进度。
 */
class NarrationService : Service() {
    /** 一句话；新版网页还带着它在书里的位置（loc 和进度里的 location 一样，pct 是 0～1 的百分比） */
    data class Seg(val id: String, val text: String, val loc: String = "", val pct: Double = -1.0)

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
    private var cover: Bitmap? = null
    private var voice = ""
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
                webGen = Events.generation
                orphan = false
                posSeg = null
                listenMs = 0
                lastSave = SystemClock.elapsedRealtime()
                voice = j.optString("voice")
                speed = j.optDouble("speed", 1.0)
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
                add(try { JSONObject(arg).optJSONArray("segments") } catch (e: Exception) { null })
                needSent = false
                if (waitingMore && playing) { waitingMore = false; index = minOf(index, queue.size - 1); playCurrent() }
            }
            "end" -> {
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
                voice = arg
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
            queue += Seg(id, text, o.optString("loc"), if (o.has("pct")) o.optDouble("pct", -1.0) else -1.0)
        }
    }

    // ---------- 播放 ----------

    private fun playCurrent() {
        if (!playing) return
        val seg = queue.getOrNull(index)
        if (seg == null) {
            when {
                ended -> stop(if (timerMin == -1) "本章读完了" else "全书读完了")
                // 网页没了，没人给下一段
                !webAlive() -> stop(webGoneMessage())
                else -> { waitingMore = true; status = "加载下一章…"; publish(); askMore() }
            }
            return
        }
        Events.emit("tts", JSONObject().put("type", "seg").put("id", seg.id))
        tick()
        if (!webAlive()) notePosition(seg)
        WearSync.pushState(this, stateJson())
        askMore()
        if (useDevice) return speakDevice(seg)
        val job = audioFor(seg)
        if (!job.isDone) { status = "合成中…"; publish() }
        prefetch()
        pool.execute {
            val file = try { job.get() } catch (e: Exception) { null }
            handler.post {
                if (queue.getOrNull(index)?.id != seg.id || !playing) return@post
                if (file == null) fallback("书库语音合成失败") else startPlayer(seg, file)
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
            val v = voice
            val s = speed
            pool.submit<File> {
                val key = sha1("$v|$s|${seg.text}")
                val f = File(cacheDir, "tts/$key.mp3")
                if (f.exists() && f.length() > 0) return@submit f
                val body = JSONObject().put("text", seg.text).put("voice", v).put("speed", s)
                val r = Http.postJson(app.link.url("/api/tts"), app.config.token, body, timeoutMs = 60000)
                if (!r.ok) throw IOException(r.errorMessage())
                f.parentFile?.mkdirs()
                f.writeBytes(r.body)
                f
            }
        }
    }

    // 预先合成后面两句，避免句子之间停顿
    private fun prefetch() {
        for (k in 1..2) queue.getOrNull(index + k)?.let { audioFor(it) }
        synchronized(audioJobs) {
            val keep = (index - 2..index + 3).mapNotNull { queue.getOrNull(it)?.id }.toSet()
            audioJobs.keys.filter { it !in keep }.forEach { audioJobs.remove(it)?.cancel(false) }
        }
    }

    /** 快读完手上的句子时向网页要下一段。网页没了（App 被划掉、网页重建过）就不要了，读完手上的就停 */
    private fun askMore() {
        if (ended || needSent || queue.size - index > 6 || !webAlive()) return
        needSent = true
        Events.emit("tts", JSONObject().put("type", "need"))
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

    /** 书库语音不可用（NAS 连不上微软、断网）时改用手机自带语音 */
    private fun fallback(why: String) {
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
        // 正在等网页给下一章：不会有了
        if (waitingMore) stop(webGoneMessage())
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
        try { app.progress.save(id, body.toString()) } catch (e: Exception) {}
    }

    /** 听书时长：网页在的时候网页自己算阅读时长，这里只算网页不在的时候 */
    private fun tick() {
        val now = SystemClock.elapsedRealtime()
        if (lastTick > 0 && !webAlive()) listenMs += (now - lastTick).coerceIn(0, 120_000)
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
                        Events.emit("tts", JSONObject().put("type", "error").put("message", "手机没有可用的语音引擎，请在系统设置里安装“Google 文字转语音”等"))
                        pause()
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
        val han = seg.text.any { Character.UnicodeScript.of(it.code) == Character.UnicodeScript.HAN }
        val kana = seg.text.any { Character.UnicodeScript.of(it.code) == Character.UnicodeScript.HIRAGANA || Character.UnicodeScript.of(it.code) == Character.UnicodeScript.KATAKANA }
        val loc = when { kana -> Locale.JAPANESE; han -> Locale.SIMPLIFIED_CHINESE; else -> Locale.US }
        val picked = if (voice.isNotEmpty()) t.voices?.firstOrNull { it.name == voice && it.locale.language == loc.language } else null
        if (picked != null) t.voice = picked else if (t.voice?.locale?.language != loc.language) t.language = loc
        t.setSpeechRate(speed.toFloat())
        utterance = seg.id
        acquireLocks()
        t.speak(seg.text, TextToSpeech.QUEUE_FLUSH, Bundle(), seg.id)
        status = ""
        publish()
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

    private fun publish() {
        if (stopped) return // 停止以后不要再把通知和手表状态带回来
        Events.emit("tts", stateJson().put("type", "state"))
        val actions = PlaybackState.ACTION_PLAY or PlaybackState.ACTION_PAUSE or PlaybackState.ACTION_PLAY_PAUSE or
            PlaybackState.ACTION_SKIP_TO_NEXT or PlaybackState.ACTION_SKIP_TO_PREVIOUS or PlaybackState.ACTION_STOP
        session.setPlaybackState(PlaybackState.Builder().setActions(actions)
            .setState(if (playing) PlaybackState.STATE_PLAYING else PlaybackState.STATE_PAUSED, index.toLong() * 1000, if (playing) 1f else 0f).build())
        val text = queue.getOrNull(index)?.text ?: ""
        session.setMetadata(MediaMetadata.Builder()
            .putString(MediaMetadata.METADATA_KEY_TITLE, book.optString("title"))
            .putString(MediaMetadata.METADATA_KEY_ARTIST, book.optString("author"))
            .putString(MediaMetadata.METADATA_KEY_ALBUM, "书库 · 听书")
            .putString(MediaMetadata.METADATA_KEY_DISPLAY_SUBTITLE, text.take(60))
            .apply { cover?.let { putBitmap(MediaMetadata.METADATA_KEY_ALBUM_ART, it) } }
            .build())
        getSystemService(android.app.NotificationManager::class.java)?.notify(NOTIF_ID, notification())
        WearSync.pushState(this, stateJson())
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

    companion object {
        @Volatile var instance: NarrationService? = null
            private set
        private val pending = ConcurrentLinkedQueue<Pair<String, String>>()
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
