package app.shuku

import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.SystemClock

/**
 * 下载书到手机、把分享的文件传到书库、下载 App 新版本时开着的前台服务（dataSync）：
 * 切到后台、熄屏时系统不会冻结或回收 App，通知栏显示进度。
 * 服务本身不干活，只是“撑着”：活在 Downloads、ShareActivity、Updater 自己的线程里做，
 * 开始时 begin、做完 end，所有任务都做完了服务自己停掉。
 */
class TransferService : Service() {
    private var lastStartId = 0
    private var foreground = false

    override fun onCreate() {
        super.onCreate()
        instance = this
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        lastStartId = startId
        // startForegroundService 之后必须马上调用 startForeground（哪怕任务已经做完了），否则系统会让 App 闪退
        try {
            val n = build(this)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) startForeground(NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
            else startForeground(NOTIF_ID, n)
            foreground = true
        } catch (e: Exception) {
            // 开不了前台服务：任务照常做，只是切到后台后可能被系统暂停
        }
        stopIfIdle()
        return START_NOT_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    /** Android 15 起 dataSync 前台服务一天最多 6 小时：到点了退出前台（任务照常做，通知还在） */
    override fun onTimeout(startId: Int, fgsType: Int) {
        try { stopForeground(STOP_FOREGROUND_DETACH) } catch (e: Exception) {}
        foreground = false
        stopSelf()
    }

    override fun onDestroy() {
        if (instance === this) instance = null
        super.onDestroy()
    }

    /** 没有任务了就停掉。只在主线程调用；用 stopSelfResult：停之前又有人 begin 的话，服务会留着 */
    private fun stopIfIdle() {
        if (synchronized(jobs) { jobs.isNotEmpty() }) return
        if (foreground) {
            try { stopForeground(STOP_FOREGROUND_REMOVE) } catch (e: Exception) {}
            foreground = false
        }
        stopSelfResult(lastStartId)
    }

    private class Job(@Volatile var title: String, @Volatile var text: String, @Volatile var progress: Double, val upload: Boolean)

    companion object {
        private const val NOTIF_ID = 21
        @Volatile private var instance: TransferService? = null

        /** 正在做的任务（按开始的先后），通知栏显示第一个 */
        private val jobs = LinkedHashMap<String, Job>()
        private var refreshPosted = false
        @Volatile private var lastRefresh = 0L

        /**
         * 开始一个任务（key 每次都要不一样）。要在 App 界面还在前台时调用：Android 12 起 App 在后台时系统不让开前台服务，
         * 这时任务照做，只是没有前台服务撑着
         */
        fun begin(ctx: Context, key: String, title: String, text: String = "", upload: Boolean = false) {
            synchronized(jobs) { jobs[key] = Job(title, text, -1.0, upload) }
            try {
                ctx.startForegroundService(Intent(ctx, TransferService::class.java))
            } catch (e: Exception) {
                // ForegroundServiceStartNotAllowedException 等：不影响任务本身
            }
            refresh(true)
        }

        /** 更新进度（progress 0～1，小于 0 表示不知道总量）；通知最多半秒刷新一次 */
        fun update(key: String, text: String, progress: Double, title: String? = null) {
            synchronized(jobs) {
                val j = jobs[key] ?: return
                j.text = text
                j.progress = progress
                if (!title.isNullOrEmpty()) j.title = title
            }
            refresh(false)
        }

        fun end(key: String) {
            synchronized(jobs) { jobs.remove(key) }
            refresh(true)
        }

        private fun refresh(now: Boolean) {
            val app = ShukuApp.app
            synchronized(jobs) {
                if (!now && refreshPosted) return
                refreshPosted = true
            }
            val wait = if (now) 0L else maxOf(0L, 500 - (SystemClock.uptimeMillis() - lastRefresh))
            app.main.postDelayed({
                synchronized(jobs) { refreshPosted = false }
                lastRefresh = SystemClock.uptimeMillis()
                val nm = app.getSystemService(NotificationManager::class.java)
                if (synchronized(jobs) { jobs.isEmpty() }) {
                    instance?.stopIfIdle()
                    try { nm?.cancel(NOTIF_ID) } catch (e: Exception) {}
                } else {
                    // 前台服务开着时更新它的通知；没开成（App 在后台时开始的任务）就当普通通知显示
                    try { nm?.notify(NOTIF_ID, build(app)) } catch (e: Exception) {}
                }
            }, wait)
        }

        private fun build(ctx: Context): Notification {
            val list = synchronized(jobs) { jobs.values.toList() }
            val first = list.firstOrNull()
            val open = PendingIntent.getActivity(ctx, 0, Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP), PendingIntent.FLAG_IMMUTABLE)
            val b = Notification.Builder(ctx, ShukuApp.CH_TASK)
                .setSmallIcon(if (first?.upload == true) android.R.drawable.stat_sys_upload else android.R.drawable.stat_sys_download)
                .setContentTitle(first?.title ?: "书库")
                .setContentText(first?.text ?: "")
                .setContentIntent(open)
                .setOngoing(true)
                .setOnlyAlertOnce(true)
                .setShowWhen(false)
                .setCategory(Notification.CATEGORY_PROGRESS)
            if (first != null) {
                val p = first.progress
                b.setProgress(100, if (p >= 0) (p * 100).toInt().coerceIn(0, 100) else 0, p < 0)
            }
            if (list.size > 1) b.setSubText("还有 ${list.size - 1} 个")
            return b.build()
        }

        fun mb(bytes: Long): String = String.format(java.util.Locale.US, "%.1f MB", bytes / 1048576.0)
    }
}
