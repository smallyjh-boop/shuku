package app.shuku

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.os.Build
import android.os.Handler
import android.os.Looper
import app.shuku.core.Config
import app.shuku.core.Http
import app.shuku.core.ServerLink
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

/** 全局对象：连接信息、线路、离线书库、下载、进度同步 */
class ShukuApp : Application() {
    lateinit var config: Config
    lateinit var link: ServerLink
    lateinit var store: OfflineStore
    lateinit var webCache: WebCache
    lateinit var progress: ProgressSync
    lateinit var downloads: Downloads
    val io: ExecutorService = Executors.newFixedThreadPool(4)
    val main = Handler(Looper.getMainLooper())

    override fun onCreate() {
        super.onCreate()
        app = this
        Http.userAgent = "ShukuApp/${BuildConfig.VERSION_NAME} (Android ${Build.VERSION.RELEASE}; ${Build.MODEL})"
        config = Config(this)
        link = ServerLink(config)
        store = OfflineStore(this)
        webCache = WebCache(this)
        progress = ProgressSync(this)
        downloads = Downloads(this)
        createChannels()
        watchNetwork()
    }

    private fun createChannels() {
        val nm = getSystemService(NotificationManager::class.java)!!
        nm.createNotificationChannel(NotificationChannel(CH_PLAY, "听书", NotificationManager.IMPORTANCE_LOW).apply {
            description = "朗读时显示播放控制"
            setShowBadge(false)
        })
        nm.createNotificationChannel(NotificationChannel(CH_TASK, "下载和上传", NotificationManager.IMPORTANCE_LOW).apply {
            description = "下载书到手机、把文件加入书库的进度"
            setShowBadge(false)
        })
    }

    /** 网络变化（连上 / 断开 Wi-Fi、开关 Tailscale）时重新选线路，并告诉网页 */
    private fun watchNetwork() {
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        cm.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = changed()
            override fun onLost(network: Network) = changed()
        })
    }

    private var pending: Runnable? = null
    private fun changed() {
        pending?.let { main.removeCallbacks(it) }
        val r = Runnable {
            io.execute {
                if (config.configured) link.probe()
                Events.emit("network", "{}")
                progress.flush()
                downloads.autoDownload()
            }
        }
        pending = r
        main.postDelayed(r, 1500)
    }

    companion object {
        lateinit var app: ShukuApp
            private set
        const val CH_PLAY = "playback"
        const val CH_TASK = "tasks"
    }
}
