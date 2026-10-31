package app.shuku

import android.app.ActivityManager
import android.content.Context
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 诊断记录：App 的几件关键的事（回到前台、网页打不开、网页进程没了、主线程卡住）记在手机上的一个小文件里，
 * “设置 → 手机 App”里能看到。老平板（armv7、内存小）上出过“隔夜再打开，显示连接书库以后就卡死”，
 * 现场拿不到，下次再出问题时看这里就知道卡在哪里。
 *
 * 主线程卡住检测：后台线程每 5 秒往主线程丢一个小任务，5 秒内没执行就把主线程当时在做什么（调用栈）记下来。
 */
object Diag {
    private const val MAX_LINES = 120
    private val fmt = SimpleDateFormat("MM-dd HH:mm:ss", Locale.US)
    private val lock = Any()
    private lateinit var file: File

    /** 内存小或者只有 32 位（armv7）的老设备：后台少占内存、少干活 */
    var lowEnd = false
        private set

    fun init(ctx: Context) {
        file = File(ctx.filesDir, "diag.log")
        val am = ctx.getSystemService(Context.ACTIVITY_SERVICE) as ActivityManager
        val mi = ActivityManager.MemoryInfo()
        try { am.getMemoryInfo(mi) } catch (e: Exception) {}
        lowEnd = am.isLowRamDevice || Build.SUPPORTED_64_BIT_ABIS.isEmpty() || (mi.totalMem in 1 until 3L * 1024 * 1024 * 1024)
        log("启动 · 安卓 ${Build.VERSION.RELEASE} · ${Build.SUPPORTED_ABIS.firstOrNull() ?: "?"} · 内存 ${mi.totalMem / 1048576} MB" +
            (if (lowEnd) " · 按低配设备处理" else "") + " · 网页组件 ${EInk.webViewVersion()}")
        startWatchdog()
    }

    /** 记一行（任何线程都能调用；只是追加一行小文本，很快） */
    fun log(msg: String) {
        if (!::file.isInitialized) return
        synchronized(lock) {
            try {
                file.appendText(fmt.format(Date()) + "  " + msg.replace('\n', ' ') + "\n")
                if (file.length() > 48 * 1024) {
                    val lines = file.readLines()
                    file.writeText(lines.takeLast(MAX_LINES).joinToString("\n", postfix = "\n"))
                }
            } catch (e: Exception) {}
        }
    }

    fun read(): String = synchronized(lock) {
        try { if (::file.isInitialized && file.exists()) file.readLines().takeLast(MAX_LINES).joinToString("\n") else "" } catch (e: Exception) { "" }
    }

    fun clear() = synchronized(lock) { try { file.delete() } catch (e: Exception) {} }

    private fun startWatchdog() {
        val main = Handler(Looper.getMainLooper())
        Thread({
            var reported = false
            while (true) {
                val done = AtomicBoolean(false)
                val sent = SystemClock.uptimeMillis()
                main.post { done.set(true) }
                try { Thread.sleep(5000) } catch (e: InterruptedException) { return@Thread }
                if (done.get()) { reported = false; continue }
                // 主线程 5 秒没反应：记下它在做什么（一次卡住只记一次，恢复后再记恢复了多久）
                if (!reported) {
                    reported = true
                    val st = Looper.getMainLooper().thread.stackTrace.take(14).joinToString(" < ") { "${it.className.substringAfterLast('.')}.${it.methodName}:${it.lineNumber}" }
                    log("主线程卡住了 5 秒以上：$st")
                }
                // 等它恢复
                while (!done.get()) {
                    try { Thread.sleep(1000) } catch (e: InterruptedException) { return@Thread }
                }
                log("主线程恢复了，一共卡了 ${(SystemClock.uptimeMillis() - sent) / 1000} 秒")
                reported = false
            }
        }, "shuku-watchdog").apply { isDaemon = true; priority = Thread.MIN_PRIORITY }.start()
    }
}
