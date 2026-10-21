package app.shuku.wear

import app.shuku.core.BookPos
import app.shuku.core.BookText
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
import java.util.Locale
import java.util.concurrent.CancellationException
import java.util.concurrent.CompletableFuture
import java.util.concurrent.CompletionException
import java.util.concurrent.Executors
import kotlin.math.abs
import kotlin.math.max
import kotlin.math.min

/**
 * 在手表上听书：把这一章按句切开，用书库的自然语音（POST /api/tts，和手机、网页听书同一个音色）合成，
 * 手表自己播放——蓝牙耳机，或者用户同意后用手表扬声器。书库的语音用不了（连不上书库、设置成了设备语音）
 * 时改用手表自带的语音。读完一章接着读下一章；进度和阅读页一样传回书库，手机、网页能接着读。
 * 锁屏、表盘上的媒体控制、耳机按键都能控制（MediaSession）。
 *
 * 离线听书：开始听时就把后面几章的正文取到手表上；播放时提前合成后面几分钟的语音存在手表上（ListenStore），
 * 用户点了“多准备一些（离线听）”才再多准备约半小时。离开手机、没网时接着听；准备好的听完了，再用手表自带语音读存好的正文。
 *
 * 省电：只在播放时提前合成（暂停就取消排队的），离线听书包只在用户点了以后下载；暂停、等耳机时不拿唤醒锁，
 * 暂停 10 分钟没再听就关掉；没有定时轮询（等网络变化、等合成结果都是回调）；走系统默认网络，不强开 Wi-Fi。
 */
class ListenService : Service() {
    private class Chap(val i: Int, val data: JSONObject, val title: String, val len: Int)

    /** 一句。seq：加进队列的顺序号，一直往上加（队列前面删掉一段后也能比较先后） */
    private class Seg(val chap: Chap, val n: Int, val text: String, val start: Int, val seq: Long)

    /** 一句的语音文件。fresh：刚请书库合成的（不是手表上本来就有的），说明书库的语音能用 */
    private class Clip(val file: File, val bytes: Long, val fresh: Boolean = false)

    /** 一句的合成任务。far：提前准备离线听的，单独一个线程排队，不挡着马上要读的句子 */
    private class Job(val future: CompletableFuture<Clip>, val far: Boolean)
    private class Start(val toc: JSONObject, val ci: Int, val frac: Double, val ch: JSONObject)

    /** 提前准备时离线听书的空间满了 */
    private class PackFull : IOException("离线听书的空间满了")

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
        val offlineSec: Int = 0, // 手表上已经准备好、没网也能接着听的语音有多长
        val preparing: Boolean = false, // 正在提前合成
        val more: Boolean = false, // 选了“多准备一些”
        val online: Boolean = true,
        val posAt: Long = 0, // 读到这一句的时间（阅读页据此跟着翻）
        val chapterCount: Int = 0, // 这本书一共几章（还没打开时是 0）
    )

    /** 正在听的书（给 Tile、表盘复杂功能看，哪个线程都能读） */
    class Now(val bookId: Long, val title: String, val playing: Boolean, val percent: Double)

    /** 听书要用到的章节（第 from～to 章），BookFiles 清理缓存时留着 */
    class Keep(val bookId: Long, val fileId: Long, val from: Int, val to: Int)

    private val handler = Handler(Looper.getMainLooper())
    private val synthPool = Executors.newFixedThreadPool(2) // 马上要读的几句
    private val farPool = Executors.newSingleThreadExecutor() // 提前准备离线听的，一句一句来
    private val io = Executors.newSingleThreadExecutor()
    private val app get() = WearApp.app

    private lateinit var session: MediaSession
    private lateinit var audio: AudioManager
    private var focusRequest: AudioFocusRequest? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private var packLock: PowerManager.WakeLock? = null // 暂停时用户要准备离线听书：下载完就放掉

    private var bookId = 0L
    private var title = ""
    private var author = ""
    private var fileId = 0L
    private var chapters = JSONArray()
    private var files: BookFiles? = null
    private var startChap = 0
    private var startFrac = 0.0

    private val queue = ArrayList<Seg>()
    private var nextSeq = 0L
    private var index = 0
    private var lastChap = -1 // 已经取到的最后一章
    private var playing = false
    private var needOutput = false
    private var loadingNext = false
    private var waitingNext = false
    private var noMore = false
    private var textRetryAt = 0L // 取后面的章节失败了（没网、手表上没存）：过一会儿再试
    private var status = ""
    private var speed = 1.0
    private var more = false // 这次听书时用户点了“多准备一些（离线听）”（不记住，下次要再点）
    private var timerMin = 0 // 0 不定时，>0 分钟，-1 读完本章
    private var timerEnd = 0L
    private var pausedByFocus = false
    private var stopped = false
    private var gen = 0 // 每次重新开始（换书、换位置）加一，旧的后台结果作废
    private var playToken = 0

    // 书库语音用不了时改用手表自带语音；不是设置成设备语音的话，读几十句后在后台再试一次书库
    private var useDevice = false
    private var deviceOnly = false
    private var deviceCount = 0
    private var serverError = ""
    private var errorsInRow = 0

    private var player: MediaPlayer? = null
    private var playerSeg: Seg? = null
    private val audioJobs = HashMap<Seg, Job>()
    private var farBlockedUntil = 0L // 提前合成失败了（没网、空间满了）：这之前先不提前合成
    private var farFull = false
    private var offlineSec = 0
    private var preparing = false
    private var bytesPerSec = 6000.0 // 语音每秒多少字节（书库的微软语音是 48kbps），播放时按实际校正
    private var tts: TextToSpeech? = null
    private var ttsReady = false
    private var utterance: String? = null

    private var listenSince = 0L
    private var lastSave = 0L
    private var savedChap = -1
    private var savedSeq = -1L // 上次存进度（或开始听）时的那一句
    private var keptChap = -1
    private var nowSig = ""
    private var notifSig = ""
    private var posAt = 0L // 读到现在这一句的时间

    // ---------- 生命周期 ----------

    override fun onCreate() {
        super.onCreate()
        instance = this
        speed = app.config.prefs.getFloat("listen_speed", 1f).toDouble()
        bytesPerSec = app.config.prefs.getFloat("listen_bps", 6000f).toDouble().coerceIn(2000.0, 40000.0)
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
        val old = File(cacheDir, "tts") // 旧版放语音的地方
        app.io.execute { old.deleteRecursively() }
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
        if (instance === this) {
            instance = null
            now = null
            keep = null
        }
        gen++
        try { unregisterReceiver(noisy) } catch (e: Exception) {}
        audio.unregisterAudioDeviceCallback(devices)
        handler.removeCallbacksAndMessages(null)
        releasePlayer()
        tts?.shutdown()
        session.release()
        focusRequest?.let { audio.abandonAudioFocusRequest(it) }
        releaseLock()
        releasePackLock()
        clearAudio()
        synthPool.shutdownNow()
        farPool.shutdownNow()
        io.shutdownNow()
        Today.updateViews()
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

    /**
     * 从第 i 章的 frac 处开始读；i < 0 表示从这本书上次的位置接着读（联网时用书库的位置，先补传离线时的进度）。
     * play：开始后马上读（书库上的位置被别处更新、跟过去时保持原来的播放/暂停）；jump：只是跟到别处读到的位置。
     */
    private fun begin(id: Long, bookTitle: String, i: Int, frac: Double, play: Boolean = true, jump: Boolean = false) {
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
        textRetryAt = 0
        farBlockedUntil = 0
        offlineSec = 0
        preparing = false
        keptChap = -1
        if (id != bookId) {
            chapters = JSONArray()
            fileId = 0
            author = ""
            // 换了一本书：书库的语音重新试；离线听书要再点才准备
            useDevice = false
            deviceOnly = false
            deviceCount = 0
            serverError = ""
            more = false
        }
        bookId = id
        title = bookTitle
        val f = BookFiles(this, id)
        files = f
        startChap = i
        startFrac = frac
        handler.removeCallbacks(idleStop)
        status = if (jump) "跟到别处读到的地方…" else "准备中…"
        playing = play
        pausedByFocus = false
        session.isActive = true
        if (play) {
            requestFocus()
            acquireLock()
            listenSince = SystemClock.elapsedRealtime()
        } else listenSince = 0
        publish()
        if (play && !jump) pausePhone()
        io.execute {
            val r = runCatching {
                val toc: JSONObject
                var ci: Int
                var fr: Double
                if (i < 0) {
                    // 接着上次读（Tile、表盘上的“继续听”）：先把离线时记下的进度传上去，再看书库上读到了哪里
                    app.flushPending()
                    toc = f.toc()
                    val p = f.openPoint(toc)
                    ci = p.first
                    fr = p.second
                } else {
                    // 阅读页刚取过目录，一般不用再联网
                    toc = f.recentToc(10 * 60_000L)
                    ci = i
                    fr = frac
                }
                val chs = toc.optJSONArray("chapters") ?: JSONArray()
                if (chs.length() == 0) throw IOException("这本书没有可以朗读的文字")
                ci = ci.coerceIn(0, chs.length() - 1)
                Start(toc, ci, fr, f.chapter(ci, toc.optLong("file_id"), chs.optJSONObject(ci)))
            }
            // 换书了就删掉别的书准备的语音；这本书前面听过的也删掉
            r.getOrNull()?.let { s -> runCatching { ListenStore.prepare(this, id, s.toc.optLong("file_id"), s.ci - 1) } }
            handler.post {
                if (my != gen || stopped) return@post
                val s = r.getOrElse {
                    if (it is WearApp.Unauthorized) {
                        stop(it.message)
                        return@post
                    }
                    playing = false
                    listenSince = 0
                    status = "打开失败：" + (it.message ?: "连不上书库")
                    releaseLock()
                    idleLater(IDLE_MS)
                    publish()
                    return@post
                }
                fileId = s.toc.optLong("file_id")
                chapters = s.toc.optJSONArray("chapters") ?: JSONArray()
                if (title.isEmpty()) title = s.toc.optString("title")
                author = s.toc.optString("author")
                startChap = s.ci
                startFrac = s.frac
                val segs = segsOf(s.ci, s.ch)
                lastChap = s.ci
                queue.addAll(segs)
                // 从 frac 所在的那一句开始
                val at = (s.frac * (segs.firstOrNull()?.chap?.len ?: 0)).toInt()
                index = segs.indexOfLast { it.start <= at }.coerceAtLeast(0)
                status = ""
                updateKeep(s.ci)
                Today.noteBook(bookId, title)
                // 开始的位置不用马上存回去（就是书库上的、或者阅读页刚存的）：听了一阵、换了章再存，
                // 免得白白更新书库上的进度版本，让正在别的设备上读的人被跳回来
                savedChap = s.ci
                savedSeq = queue.getOrNull(index)?.seq ?: -1L
                lastSave = SystemClock.elapsedRealtime()
                posAt = System.currentTimeMillis()
                if (playing) playCurrent()
                else {
                    queue.getOrNull(index)?.let { markPos(it) }
                    moreText()
                    prefetch()
                    publish()
                }
            }
        }
    }

    private fun segsOf(i: Int, ch: JSONObject): List<Seg> {
        val text = BookText.format(ch.optString("text"))
        ch.remove("text") // 只留 start、chars、total 算进度，正文不占内存
        val chap = Chap(i, ch, ch.optString("title"), text.length)
        return BookText.sentences(text).mapIndexed { n, s -> Seg(chap, n, s.text, s.start, nextSeq++) }
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
            if (!useDevice && !deviceOnly) audioFor(seg, far = false)
            moreText()
            prefetch() // 只合成马上要读的几句，连上耳机就能开始
            idleLater(IDLE_MS)
            publish()
            return
        }
        needOutput = false
        acquireLock()
        moreText()
        // 已经合成好的（包括提前准备的离线听书）：直接放
        readyClip(seg)?.takeIf { it.file.exists() }?.let {
            startPlayer(seg, it.file)
            prefetch()
            return
        }
        if (useDevice) {
            // 书库语音用不了：离线听书包里有这一句（包括别的语速的）就放它，没有就用手表自带语音读
            val f = packFile(seg)
            if (f != null) {
                startPlayer(seg, f)
                prefetch()
                return
            }
            deviceCount++
            if (!deviceOnly && deviceCount % 30 == 0) retryServer()
            speakDevice(seg)
            prefetch()
            return
        }
        val job = audioFor(seg, far = false)
        if (!job.future.isDone) {
            status = "合成中…"
            publish()
        }
        prefetch()
        val my = ++playToken // 同一句等合成时又点了继续：只按最后一次的来，不重复开播
        job.future.whenComplete { c, e ->
            handler.post {
                if (my != playToken || !playing || stopped || queue.getOrNull(index) !== seg) return@post
                if (c != null) startPlayer(seg, c.file) else fallback(unwrap(e))
            }
        }
    }

    private fun startPlayer(seg: Seg, file: File) {
        releasePlayer()
        val p = MediaPlayer()
        try {
            p.setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA).setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
            p.setWakeMode(this, PowerManager.PARTIAL_WAKE_LOCK)
            p.setDataSource(file.path)
            p.setOnCompletionListener { if (player === p) advance() }
            p.setOnErrorListener { _, _, _ ->
                if (player === p) {
                    releasePlayer()
                    dropBroken(seg, file)
                    if (++errorsInRow >= 3) pauseAt("音频无法播放") else advance()
                }
                true
            }
            p.prepare()
            // 按实际的时长校正“每秒多少字节”，算离线能听多久
            val ms = p.duration
            if (ms > 1000 && file.length() > 0) bytesPerSec = bytesPerSec * 0.8 + file.length() * 1000.0 / ms * 0.2
            p.start()
            player = p
            playerSeg = seg
            errorsInRow = 0
            status = ""
            publish()
        } catch (e: Exception) {
            // 文件坏了：删掉重新合成（没网时改用手表语音）
            if (player === p) {
                player = null
                playerSeg = null
            }
            p.release()
            dropBroken(seg, file)
            if (++errorsInRow >= 3) {
                if (useDevice) pauseAt("音频无法播放") else fallback(e)
            } else playCurrent()
        }
    }

    // 放不了的语音文件：马上删掉（下面紧接着重试，不能再拿到它）
    private fun dropBroken(seg: Seg, file: File) {
        audioJobs.remove(seg)?.future?.cancel(false)
        ListenStore.delete(file)
    }

    /** 这一句已经合成好了 */
    private fun readyClip(seg: Seg): Clip? {
        val f = audioJobs[seg]?.future ?: return null
        if (!f.isDone || f.isCompletedExceptionally) return null
        return f.getNow(null)
    }

    /** 离线听书包里这一句的语音（这个语速的，没有就用别的语速的） */
    private fun packFile(seg: Seg): File? {
        val f = ListenStore.file(this, bookId, fileId, seg.chap.i, seg.n, speed, seg.text)
        if (f.length() > 0) return f
        return ListenStore.otherSpeeds(this, bookId, fileId, seg.chap.i, seg.n, speed, seg.text).firstOrNull { it.length() > 0 }
    }

    /**
     * 合成一句（后台排队）。每句一个文件（书、文件、章、句、语速都在文件名里），文字相同的两句互不影响。
     * far：提前准备离线听的，排在单独的线程里。localOnly：只看手表上有没有（没网、用手表语音时，
     * 找出已经准备好的离线听书，不去请书库合成）。
     */
    private fun audioFor(seg: Seg, far: Boolean, localOnly: Boolean = false): Job {
        audioJobs[seg]?.let { j ->
            // 失败了的重来；提前准备的还在排队，而这句马上要读了：改到近处的线程
            val retry = j.future.isCompletedExceptionally || (!far && j.far && !j.future.isDone)
            if (!retry) return j
            j.future.cancel(false)
        }
        val s = speed
        val bid = bookId
        val fid = fileId
        val fut = CompletableFuture.supplyAsync({ synth(seg, s, bid, fid, far, localOnly) }, if (far) farPool else synthPool)
        val job = Job(fut, far)
        audioJobs[seg] = job
        fut.whenComplete { c, e -> handler.post { onJobDone(seg, job, c, e) } }
        return job
    }

    /** 在后台线程：手表上有就直接用，没有就请书库合成，存进离线听书包 */
    private fun synth(seg: Seg, s: Double, bid: Long, fid: Long, far: Boolean, localOnly: Boolean): Clip {
        val f = ListenStore.file(this, bid, fid, seg.chap.i, seg.n, s, seg.text)
        if (f.length() > 0) return Clip(f, f.length())
        val others = ListenStore.otherSpeeds(this, bid, fid, seg.chap.i, seg.n, s, seg.text)
        fun other(): Clip? = others.firstOrNull { it.length() > 0 }?.let { Clip(it, it.length()) }
        if (localOnly || !app.netUsable()) {
            other()?.let { return it }
            if (localOnly) throw IOException("这一句还没准备好")
        }
        if (far && ListenStore.bytes(this) >= ListenStore.MAX_BYTES) throw PackFull()
        val r = try {
            app.api("POST", "/api/tts", JSONObject().put("text", seg.text).put("voice", "").put("speed", s), 60000)
        } catch (e: WearApp.Unauthorized) {
            throw e
        } catch (e: IOException) {
            other()?.let { return it }
            throw e
        }
        if (!r.ok) {
            other()?.let { return it }
            throw IOException(r.errorMessage())
        }
        ListenStore.write(this, f, r.body, others)
        return Clip(f, r.body.size.toLong(), fresh = true)
    }

    private fun onJobDone(seg: Seg, job: Job, c: Clip?, e: Throwable?) {
        if (stopped) return
        val cause = unwrap(e)
        if (c != null) {
            // 书库的语音又能用了：接着提前准备
            if (c.fresh && farBlockedUntil > 0 && !farFull) {
                farBlockedUntil = 0
                prefetch()
            }
        } else if (job.far && cause !is CancellationException && audioJobs[seg] === job) {
            // 提前准备的没合成成功：先停一会儿，别一句接一句地失败
            audioJobs.remove(seg)
            farFull = cause is PackFull
            farBlockedUntil = SystemClock.elapsedRealtime() + if (farFull) 30_000L else 60_000L
        }
        scheduleOffline()
    }

    /**
     * 合成后面的句子：马上要读的三句不停顿地接上；播放时再往后提前准备几分钟（没网时也能接着听一会儿），
     * 用户点了“多准备一些”才准备约半小时（离线听书，总大小不超过上限）。读过的（上一句以前的）、超出这一段的删掉，手表空间小。
     */
    private fun prefetch() {
        if (stopped || bookId == 0L || queue.isEmpty()) return
        val curSeq = queue.getOrNull(index)?.seq ?: (queue.last().seq + 1)
        // 正在播放（或者点了播放、等着连耳机）才合成马上要读的几句
        if ((playing || needOutput) && !useDevice && !deviceOnly) for (k in 1..NEAR) queue.getOrNull(index + k)?.let { near(it) }
        val (maxSentences, maxChars) = window()
        // 往后提前准备：只在播放时；暂停时只有用户点了“多准备一些”才接着准备
        val wantFar = playing || more
        val farOk = wantFar && SystemClock.elapsedRealtime() >= farBlockedUntil
        // 没网、书库语音用不了时不请书库合成，只把手表上已经准备好的找出来（接着放、算还能离线听多久）
        val localOnly = useDevice || deviceOnly || !app.netUsable()
        var endSeq = curSeq + NEAR
        var count = 0
        var chars = 0
        for (k in index + 1 until queue.size) {
            val seg = queue[k]
            count++
            chars += seg.text.length
            if (count > maxSentences || (count > NEAR && chars > maxChars)) break
            endSeq = seg.seq
            if (farOk && audioJobs[seg] == null && (k > index + NEAR || localOnly)) audioFor(seg, far = true, localOnly = localOnly)
        }
        val drop = ArrayList<File>()
        val it = audioJobs.entries.iterator()
        while (it.hasNext()) {
            val (seg, job) = it.next()
            val keep = seg.seq >= curSeq - 1 && seg.seq <= endSeq
            if (keep && (wantFar || !job.far || job.future.isDone)) continue
            // 听过的、超出这一段的删掉；暂停了，排着队还没合成的取消（准备好的留着）
            job.future.cancel(false)
            if (!keep && job.future.isDone && !job.future.isCompletedExceptionally) job.future.getNow(null)?.let { drop += it.file }
            it.remove()
        }
        if (drop.isNotEmpty()) {
            app.io.execute { drop.forEach { f -> ListenStore.delete(f) } }
            if (farFull) farBlockedUntil = 0 // 空出地方了
        }
        scheduleOffline()
    }

    private fun near(seg: Seg) {
        val j = audioJobs[seg]
        if (j == null || (j.far && !j.future.isDone)) audioFor(seg, far = false)
    }

    /** 提前准备多少：句数、字数（按每分钟大约 260 字、语速换算）。平时几分钟；用户点了“多准备一些”约半小时 */
    private fun window(): Pair<Int, Int> {
        val minutes = if (more) PACK_MINUTES else AUTO_MINUTES
        val sentences = if (more) PACK_SENTENCES else AUTO_SENTENCES
        return sentences to (minutes * CHARS_PER_MIN * speed).toInt()
    }

    /** 停止、换书、换语速：排着队的合成全部取消（已经写好的文件留着） */
    private fun clearAudio() {
        audioJobs.values.forEach { it.future.cancel(false) }
        audioJobs.clear()
    }

    // 已经准备好、没网也能接着听的有多久；正在不在提前合成。合成完一句就要算一次，合并成一秒一次
    private val offlineTask = Runnable {
        var bytes = 0L
        var k = index
        while (k < queue.size) {
            bytes += (readyClip(queue[k]) ?: break).bytes
            k++
        }
        val sec = (bytes / bytesPerSec).toInt()
        val prep = audioJobs.values.any { it.far && !it.future.isDone }
        // 暂停时用户要准备离线听书：下载期间拿着唤醒锁（不然熄屏后就停了），下载完就放掉
        if (prep && more && !playing && !stopped) acquirePackLock() else releasePackLock()
        if (sec != offlineSec || prep != preparing) {
            offlineSec = sec
            preparing = prep
            sendBroadcast(Intent(WearApp.ACTION_LISTEN).setPackage(packageName))
        }
    }

    private fun scheduleOffline() {
        handler.removeCallbacks(offlineTask)
        handler.postDelayed(offlineTask, 1000)
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
            val b = bookId
            app.io.execute { ListenStore.clearBook(app, b) }
            stop("全书读完了")
            return
        }
        waitingNext = true
        status = "加载下一章…"
        publish()
    }

    /**
     * 取后面的章节：至少预先取好后面 4 章的正文（离开手机没网时接着听），要提前准备的语音更多时再多取一些。
     * 一次取一章，取到了接着取下一章；没网、手表上也没存的话过一分钟再试。
     */
    private fun moreText(force: Boolean = false) {
        if (loadingNext || noMore || bookId == 0L || stopped) return
        // 暂停着（又没让准备离线听）就不取
        if (!force && !playing && !needOutput && !more) return
        val ni = lastChap + 1
        if (ni >= chapters.length()) {
            noMore = true
            return
        }
        if (!force) {
            val cur = queue.getOrNull(index)?.chap?.i ?: lastChap
            val ahead = queue.size - index
            val want = ahead <= 8 || ni - cur <= MIN_AHEAD || (ahead < window().first + NEAR && ni - cur <= maxAhead())
            if (!want || SystemClock.elapsedRealtime() < textRetryAt) return
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
                    } else prefetch()
                    moreText()
                }.onFailure {
                    textRetryAt = SystemClock.elapsedRealtime() + 60_000L
                    if (waitingNext) {
                        waitingNext = false
                        pauseAt("下一章没取到：" + (it.message ?: "连不上书库"))
                    }
                }
            }
        }
    }

    private fun maxAhead() = if (more) 20 else 8

    /** 听到第 chap 章：告诉 BookFiles 留着哪几章；再往前一章之前合成的语音删掉 */
    private fun updateKeep(chap: Int) {
        if (chap == keptChap) return
        keptChap = chap
        keep = Keep(bookId, fileId, chap - 1, chap + maxAhead())
        val b = bookId
        val fid = fileId
        app.io.execute { ListenStore.dropBefore(app, b, fid, chap) }
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
        prefetch() // 排着队的提前合成取消（用户点了“多准备一些”的除外）
        idleLater(IDLE_MS)
        publish()
    }

    // 暂停（或者等着连耳机）太久没再听：关掉，不一直挂着通知。用户要的离线听书还在下载就等下完
    private val idleStop = Runnable {
        if (playing || stopped) return@Runnable
        if (more && audioJobs.values.any { it.far && !it.future.isDone }) idleLater(IDLE_MS) else stop("")
    }

    private fun idleLater(ms: Long) {
        handler.removeCallbacks(idleStop)
        handler.postDelayed(idleStop, ms)
    }

    private fun resume() {
        if (playing || stopped || bookId == 0L) return
        if (chapters.length() == 0) {
            // 上次没打开成功：重试
            app.retryNow()
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
        if (p != null && seg != null && playerSeg === seg && AudioOut.ok(this)) {
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
        if (instance === this) {
            instance = null
            now = null
            keep = null
        }
        gen++
        playing = false
        releasePlayer()
        tts?.stop()
        handler.removeCallbacks(timerTask)
        handler.removeCallbacks(idleStop)
        handler.removeCallbacks(offlineTask)
        session.isActive = false
        focusRequest?.let { audio.abandonAudioFocusRequest(it) }
        releaseLock()
        releasePackLock()
        // 排着队的合成、提前准备全部取消；已经准备好的离线听书留着（下次接着听，Tile 上点“继续听”），听过的已经删掉了
        clearAudio()
        more = false
        app.config.prefs.edit().putFloat("listen_bps", bytesPerSec.toFloat()).apply()
        Today.updateViews()
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
        serverError = e?.message ?: "合成失败"
        deviceOnly = serverError.contains("设备自带语音")
        useDevice = true
        deviceCount = 0
        playCurrent()
    }

    /**
     * 用手表语音读着的时候，后台试一下书库的语音（后面几句里还没有语音的那句）：书库真的合成出来了就换回书库语音，
     * 不用停下来等。
     */
    private fun retryServer() {
        if (deviceOnly || !app.netUsable()) return
        val next = (index + 1..index + 6).asSequence().mapNotNull { queue.getOrNull(it) }
            .firstOrNull { readyClip(it) == null && packFile(it) == null } ?: return
        val my = gen
        audioFor(next, far = false).future.whenComplete { c, _ ->
            handler.post {
                if (my != gen || stopped || c?.fresh != true || !useDevice) return@post
                useDevice = false
                deviceCount = 0
                serverError = ""
                prefetch()
                publish()
            }
        }
    }

    /** 手表的网络变了（WearApp 在主线程调用）：刚才没合成成功的、没取到的章节重新来 */
    fun onNetwork() {
        if (stopped || bookId == 0L || chapters.length() == 0) return
        textRetryAt = 0
        farBlockedUntil = 0
        val it = audioJobs.entries.iterator()
        while (it.hasNext()) if (it.next().value.future.isCompletedExceptionally) it.remove()
        if (useDevice && playing) retryServer()
        moreText()
        prefetch()
        publish()
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

    // ---------- 语速、定时、离线准备 ----------

    private fun cycleSpeed() {
        val i = SPEEDS.indexOfFirst { it >= speed - 0.01 }
        setSpeed(SPEEDS[(i + 1) % SPEEDS.size])
    }

    private fun setSpeed(v: Double) {
        if (kotlin.math.abs(v - speed) < 0.01) return
        speed = v
        app.config.prefs.edit().putFloat("listen_speed", speed.toFloat()).apply()
        // 按新语速重新合成；原来准备好的先留着，没网时接着用
        clearAudio()
        releasePlayer()
        farBlockedUntil = 0
        if (playing) {
            tts?.stop()
            playCurrent()
        } else prefetch()
        publish()
    }

    private val timerTask = Runnable {
        timerMin = 0
        timerEnd = 0
        pauseAt("定时结束")
    }

    private fun cycleTimer() = setTimer(TIMERS[(TIMERS.indexOf(timerMin) + 1) % TIMERS.size])

    private fun setTimer(v: Int) {
        timerMin = v
        handler.removeCallbacks(timerTask)
        timerEnd = 0
        if (timerMin > 0) {
            timerEnd = System.currentTimeMillis() + timerMin * 60_000L
            handler.postDelayed(timerTask, timerMin * 60_000L)
        }
        publish()
    }

    /**
     * 用户点了“多准备一些（离线听）”：往后准备约半小时的语音（暂停着也下载，下完为止）。只管这一次听书，不记住——
     * 离线听书包只在用户主动要的时候下载。关掉就取消还没下的，超出平时那几分钟的也删掉。
     */
    private fun setMore(on: Boolean) {
        more = on
        keptChap = -1
        queue.getOrNull(index)?.let { updateKeep(it.chap.i) }
        farBlockedUntil = 0
        moreText()
        prefetch()
        publish()
    }

    // ---------- 进度 ----------

    /**
     * 读到新的一句：阅读页从 state() 里拿位置跟着翻（在内存里，不用每句都写存储）；
     * 每半分钟、换章、暂停、停止时存到手表上（没网时下次打开用）并传回书库。
     */
    private fun onSegment(seg: Seg) {
        posAt = System.currentTimeMillis()
        updateKeep(seg.chap.i)
        if (seg.chap.i != savedChap || SystemClock.elapsedRealtime() - lastSave > 30_000) saveProgress()
    }

    private fun markPos(seg: Seg) {
        val frac = seg.start.toDouble() / max(1, seg.chap.len)
        app.config.prefs.edit().putString("pos_$bookId", JSONObject().put("file", fileId).put("i", seg.chap.i).put("frac", frac)
            .put("by", "listen").put("at", System.currentTimeMillis()).toString()).apply()
    }

    private fun saveProgress(done: Boolean = false) {
        if (fileId == 0L) return
        val seg = queue.getOrNull(index) ?: queue.lastOrNull() ?: return
        val frac = if (done) 1.0 else seg.start.toDouble() / max(1, seg.chap.len)
        val now = SystemClock.elapsedRealtime()
        val secs = if (listenSince > 0) ((now - listenSince) / 1000).toInt() else 0
        // 还停在上次存的那一句、也没听多久（刚开始就暂停了）：不用传，听的时长留到下次一起算
        if (!done && seg.seq == savedSeq && secs < 60) return
        if (listenSince > 0) listenSince = now
        lastSave = now
        savedChap = seg.chap.i
        savedSeq = seg.seq
        markPos(seg)
        val percent = BookPos.percent(seg.chap.data, frac, done)
        val body = JSONObject().put("file_id", fileId).put("location", BookPos.location(chapters, seg.chap.i, frac))
            .put("percent", percent).put("device", WearApp.DEVICE).put("seconds", min(600, secs))
        val id = bookId
        val my = gen
        Today.noteBook(id, title, percent)
        app.io.execute {
            val r = app.saveProgress(id, body)
            if (r.conflict) handler.post { onConflict(my) }
        }
    }

    /**
     * 书库上的位置被别的设备更新过（在手机、网页上读到了别处）：这次没有覆盖，接受书库的位置——
     * 跟到那里接着听（原来在暂停就还暂停着），不来回覆盖。
     */
    private fun onConflict(my: Int) {
        if (my != gen || stopped) return
        val f = files ?: return
        val id = bookId
        io.execute {
            val toc = runCatching { f.toc() }.getOrNull()?.takeIf { !it.optBoolean("_offline") }
            val p = toc?.let { f.openPoint(it) }
            handler.post {
                if (my != gen || stopped || id != bookId || toc == null || p == null) return@post
                val cur = queue.getOrNull(index)
                val same = cur != null && toc.optLong("file_id") == fileId && cur.chap.i == p.first &&
                    abs(cur.start.toDouble() / max(1, cur.chap.len) - p.second) < 0.02
                if (same) return@post
                Toast.makeText(this, "在别处读到了新的地方，已跟过去", Toast.LENGTH_SHORT).show()
                begin(id, title, p.first, p.second, play = playing, jump = true)
            }
        }
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

    // 暂停时下载用户要的离线听书：最多拿 15 分钟，下完、开始播放、停止时马上放掉
    private fun acquirePackLock() {
        val w = packLock ?: (getSystemService(POWER_SERVICE) as PowerManager).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "shuku:offline")
            .apply { setReferenceCounted(false) }.also { packLock = it }
        if (!w.isHeld) w.acquire(15 * 60_000L)
    }

    private fun releasePackLock() {
        try { packLock?.takeIf { it.isHeld }?.release() } catch (e: Exception) {}
    }

    // ---------- 状态：界面、表盘的媒体控制、通知、Tile ----------

    private fun snapshot(): State {
        val seg = queue.getOrNull(index)
        return State(
            active = true, bookId = bookId, title = title, chapter = seg?.chap?.title ?: "", text = seg?.text ?: "",
            playing = playing, needOutput = needOutput, status = status, speed = speed, timer = timerMin,
            timerLeftMs = if (timerEnd > 0) max(0, timerEnd - System.currentTimeMillis()) else 0,
            chapterIndex = seg?.chap?.i ?: max(0, startChap), frac = seg?.let { it.start.toDouble() / max(1, it.chap.len) } ?: startFrac,
            fileId = fileId, deviceVoice = useDevice, offlineSec = offlineSec, preparing = preparing, more = more,
            online = app.netUsable(), posAt = posAt, chapterCount = chapters.length(),
        )
    }

    private fun publish() {
        if (stopped) return
        val seg = queue.getOrNull(index)
        // 表盘上的媒体控制、通知：只在显示的内容变了（换章、播放/暂停、提示）时更新，不每读一句就叫醒系统界面
        val shown = "$playing|$needOutput|$status|$title|$author|${seg?.chap?.title}"
        if (shown != notifSig) {
            notifSig = shown
            val actions = PlaybackState.ACTION_PLAY or PlaybackState.ACTION_PAUSE or PlaybackState.ACTION_PLAY_PAUSE or
                PlaybackState.ACTION_SKIP_TO_NEXT or PlaybackState.ACTION_SKIP_TO_PREVIOUS or PlaybackState.ACTION_STOP
            session.setPlaybackState(PlaybackState.Builder().setActions(actions)
                .setState(if (playing) PlaybackState.STATE_PLAYING else PlaybackState.STATE_PAUSED, PlaybackState.PLAYBACK_POSITION_UNKNOWN, if (playing) 1f else 0f).build())
            session.setMetadata(MediaMetadata.Builder()
                .putString(MediaMetadata.METADATA_KEY_TITLE, title.ifEmpty { "书库 · 听书" })
                .putString(MediaMetadata.METADATA_KEY_ARTIST, author)
                .putString(MediaMetadata.METADATA_KEY_ALBUM, seg?.chap?.title ?: "")
                .putString(MediaMetadata.METADATA_KEY_DISPLAY_SUBTITLE, seg?.chap?.title ?: "")
                .build())
            getSystemService(NotificationManager::class.java)?.notify(NOTIF_ID, notification())
        }
        // Tile、表盘复杂功能：换书、开始/暂停时刷新
        val pct = seg?.let { BookPos.percent(it.chap.data, it.start.toDouble() / max(1, it.chap.len), false) } ?: -1.0
        now = Now(bookId, title, playing, pct)
        val listening = "$bookId|$title|$playing"
        if (listening != nowSig) {
            nowSig = listening
            Today.updateViews()
        }
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
            else -> seg?.chap?.title?.ifEmpty { null } ?: if (seg == null) "准备中…" else "正在朗读"
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

        /** 正在听的书（暂停着也算）；没在听是 null */
        @Volatile var now: Now? = null
            private set

        /** 正在听的书要留着的章节 */
        @Volatile var keep: Keep? = null
            private set

        private const val NOTIF_ID = 8
        const val ACT_START = "app.shuku.wear.LISTEN"
        const val ACT_PLAY = "app.shuku.wear.PLAY"
        const val ACT_PAUSE = "app.shuku.wear.PAUSE"
        const val ACT_NEXT = "app.shuku.wear.NEXT"
        const val ACT_PREV = "app.shuku.wear.PREV"
        const val ACT_STOP = "app.shuku.wear.STOP"
        val SPEEDS = doubleArrayOf(0.8, 1.0, 1.2, 1.4, 1.6, 1.8, 2.0)
        val TIMERS = intArrayOf(0, 15, 30, 60, -1)

        private const val NEAR = 3 // 马上要读的几句先合成好，句子之间不停顿
        private const val MIN_AHEAD = 4 // 至少预先取好后面几章的正文（文字很小）
        private const val AUTO_MINUTES = 4 // 播放时自动往后准备几分钟的语音
        private const val AUTO_SENTENCES = 40
        private const val PACK_MINUTES = 30 // 用户点了“多准备一些（离线听）”
        private const val PACK_SENTENCES = 300
        private const val CHARS_PER_MIN = 260 // 1 倍速大约每分钟读多少字
        private const val IDLE_MS = 10 * 60_000L // 暂停（或者等着连耳机）这么久没再听就关掉

        private fun unwrap(e: Throwable?): Throwable? = if (e is CompletionException) e.cause ?: e else e

        /** 从第 i 章的 frac 处开始读；i = -1：从这本书上次读到的地方接着读 */
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

        /** 接着放（暂停着的话） */
        fun play() {
            instance?.let { if (!it.playing) it.resume() }
        }

        /** 上一章 / 下一章：从那一章开头读 */
        fun chapter(ctx: Context, d: Int) {
            val st = instance?.snapshot() ?: return
            jump(ctx, st.chapterIndex + d)
        }

        /** 跳到第 i 章开头接着听（听书页长按出来的进度条） */
        fun jump(ctx: Context, i: Int) {
            val me = instance ?: return
            val st = me.snapshot()
            if (st.chapterCount == 0) return // 还没打开好，章节号还不准
            if (i < 0 || i >= st.chapterCount) return
            me.saveProgress() // 换章前先存：这段听的时长别丢了
            start(ctx, st.bookId, st.title, i, 0.0)
        }

        /** 第 i 章的标题（没有就是空） */
        fun chapterTitle(i: Int): String = instance?.chapters?.optJSONObject(i)?.optString("title").orEmpty()

        fun setSpeed(v: Double) { instance?.setSpeed(v) }
        fun setTimer(v: Int) { instance?.setTimer(v) }

        fun next() { instance?.step(1) }
        fun prev() { instance?.step(-1) }
        fun stopNow() { instance?.stop("") }
        fun cycleSpeed() { instance?.cycleSpeed() }
        fun cycleTimer() { instance?.cycleTimer() }
        fun setMore(on: Boolean) { instance?.setMore(on) }

        /** 用户同意用手表扬声器读 */
        fun useSpeaker() {
            AudioOut.speakerOk = true
            instance?.resume()
        }
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
