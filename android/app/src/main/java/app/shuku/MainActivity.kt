package app.shuku

import android.Manifest
import android.app.Activity
import android.app.DownloadManager
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.graphics.Bitmap
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.os.SystemClock
import android.speech.tts.TextToSpeech
import android.util.Log
import android.view.KeyEvent
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsetsController
import android.view.WindowManager
import android.webkit.ConsoleMessage
import android.webkit.RenderProcessGoneDetail
import android.webkit.URLUtil
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast
import android.window.OnBackInvokedDispatcher
import app.shuku.core.Config
import org.json.JSONObject

/**
 * 书库 App 的主界面：一个 WebView 打开 NAS 上的书库网页（阅读排版和电脑上一样），
 * 原生代码负责内外网切换、离线、下载、后台听书、音量键、返回键、状态栏颜色。
 */
class MainActivity : Activity() {
    lateinit var web: WebView
    private lateinit var bridge: Bridge
    private val app get() = ShukuApp.app

    /** WebView 当前打开的地址（内网或 Tailscale），只拦截这台书库的请求 */
    @Volatile private var pageOrigin: String? = null
    private var readerMode = false
    private var fileCallback: ValueCallback<Array<Uri>>? = null
    private var voicesTts: TextToSpeech? = null
    @Volatile private var voicesReady = false
    private var bgColor = 0
    private var started = false

    /** 网页渲染进程崩溃后重建 WebView 时：当前地址、上一次崩溃的时间、App 在后台时等回到前台再打开的地址 */
    private var lastUrl: String? = null
    private var lastGone = 0L
    private var reopenUrl: String? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        bridge = Bridge(this)
        web = WebView(this)
        val night = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES
        applyBars(if (night) "#14181E" else "#F8F8F6", night)
        setContentView(web)
        setupWeb()
        if (Build.VERSION.SDK_INT >= 33) {
            onBackInvokedDispatcher.registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT) { goBack() }
        }
        askNotificationPermission()
        if (!handlePairIntent(intent)) start()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handlePairIntent(intent)
    }

    override fun onStart() {
        super.onStart()
        started = true
        visibleCount++
        web.onResume()
        // 网页在后台时崩溃重建过：现在才打开（在后台就重新打开大 PDF、漫画，多半又会崩）
        reopenUrl?.let { u ->
            reopenUrl = null
            reopen(u)
        }
        if (app.config.configured) app.io.execute {
            val was = app.link.base
            if (app.link.probe() != was) Events.emit("network", "{}")
            app.progress.flush()
            if (app.link.online) Updater.checkQuietly(this)
        }
    }

    override fun onResume() {
        super.onResume()
        // 安装新版本时系统要用户确认，但通知发不出来（没给通知权限）：回到 App 时再弹
        Updater.showPendingInstall(this)
    }

    override fun onStop() {
        super.onStop()
        started = false
        visibleCount--
        // 听书时网页还要继续给后台服务供句子，不能暂停
        if (NarrationService.instance == null) web.onPause()
    }

    override fun onDestroy() {
        Events.detach(web)
        voicesTts?.shutdown()
        web.destroy()
        super.onDestroy()
    }

    // ---------- 打开书库 ----------

    fun start() {
        if (!app.config.configured) {
            pageOrigin = null
            web.loadUrl("file:///android_asset/setup.html")
            return
        }
        app.io.execute {
            val base = app.link.probe()
            val target = base ?: app.config.lastBase.ifEmpty { app.config.lan.ifEmpty { app.config.remote } }
            runOnUiThread {
                pageOrigin = originOf(target)
                web.loadUrl("$target/")
            }
            if (base != null) {
                app.config.lastBase = base
                app.progress.flush()
                app.webCache.precache()
                // 先清理读完的书（设置里打开了的话），再自动下载在读的书
                app.downloads.autoClean()
                app.downloads.autoDownload()
                Updater.checkQuietly(this)
            }
        }
    }

    fun logout() {
        val token = app.config.token
        val base = app.link.base
        if (base != null && token.isNotEmpty()) app.io.execute {
            try { app.link.send("POST", "/api/auth/logout", JSONObject()) } catch (e: Exception) {}
        }
        app.config.clear()
        getSharedPreferences("webstore", 0).edit().remove("shuku.token").apply()
        start()
    }

    // ---------- WebView ----------

    private fun setupWeb() {
        val s = web.settings
        s.javaScriptEnabled = true
        s.domStorageEnabled = true
        s.mediaPlaybackRequiresUserGesture = false
        s.mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW // 网页从 Tailscale 的 https 打开时也能访问内网的 http
        s.allowContentAccess = false
        s.allowFileAccess = false
        s.useWideViewPort = true
        s.textZoom = 100
        s.setSupportZoom(true)
        s.builtInZoomControls = true // 漫画可以双指放大
        s.displayZoomControls = false
        s.userAgentString = s.userAgentString + " ShukuApp/" + BuildConfig.VERSION_NAME
        web.setRendererPriorityPolicy(WebView.RENDERER_PRIORITY_IMPORTANT, false)
        web.addJavascriptInterface(bridge, "ShukuNative")
        Events.attach(web)

        web.webViewClient = object : WebViewClient() {
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? = intercept(request)

            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val uri = request.url
                // 页面里的框架（iframe）只放行阅读器要用的和书库自己的地址，别的一律拦下（包括 shuku://，书里的内容不能拿它重新配对）
                if (!request.isForMainFrame) return !frameAllowed(uri)
                if (uri.scheme == "shuku") { handlePairUri(uri); return true }
                if (uri.scheme == "file" || uri.scheme == "blob" || uri.scheme == "data" || originOf(uri.toString()) == pageOrigin) return false
                try { startActivity(Intent(Intent.ACTION_VIEW, uri)) } catch (e: Exception) {}
                return true
            }

            override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
                // 打开了新页面（重新加载、换地址）：原来页面里的听书就没人管了
                Events.pageStarted(view)
            }

            override fun doUpdateVisitedHistory(view: WebView, url: String?, isReload: Boolean) {
                // 记下当前地址（包括网页里 # 后面的路由），网页崩溃后重新打开用
                if (view === web && !url.isNullOrEmpty()) lastUrl = url
            }

            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                // 第一次打开就没网、手机上还没有缓存：显示连接页，可以重试
                if (view === web && request.isForMainFrame && request.url.scheme != "file") web.loadUrl("file:///android_asset/setup.html#offline")
            }

            /**
             * 大 PDF、漫画把网页的渲染进程撑爆了（或者系统内存不够把它杀了）。不处理的话系统会把整个 App 一起杀掉，
             * 后台听书也会停。这里换一个新的 WebView 重新打开当前页，返回 true 表示处理好了
             */
            override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                if (view === web) recreateWeb(view)
                return true
            }
        }
        web.webChromeClient = object : WebChromeClient() {
            override fun onShowFileChooser(view: WebView, callback: ValueCallback<Array<Uri>>, params: FileChooserParams): Boolean {
                fileCallback?.onReceiveValue(null)
                fileCallback = callback
                return try {
                    startActivityForResult(params.createIntent().putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true), REQ_FILE)
                    true
                } catch (e: Exception) {
                    fileCallback = null
                    false
                }
            }

            override fun onConsoleMessage(m: ConsoleMessage): Boolean {
                if (BuildConfig.DEBUG) Log.d("shuku-web", "${m.message()} (${m.sourceId()}:${m.lineNumber()})")
                return true
            }
        }
        // 详情里的“下载”按钮：交给系统下载管理器，存到手机的下载文件夹
        web.setDownloadListener { url, _, disposition, mime, _ ->
            try {
                val req = DownloadManager.Request(Uri.parse(url))
                    .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                    .setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, URLUtil.guessFileName(url, disposition, mime))
                (getSystemService(DOWNLOAD_SERVICE) as DownloadManager).enqueue(req)
                toast("开始下载，完成后在“下载”文件夹里")
            } catch (e: Exception) {
                toast("下载失败：${e.message}")
            }
        }
    }

    /** 书库自己的地址：当前页面的、正在用的线路、内网、Tailscale */
    private fun isOurs(origin: String): Boolean = origin.isNotEmpty() &&
        (origin == pageOrigin || origin == originOf(app.link.base ?: "") || origin == originOf(app.config.lan) || origin == originOf(app.config.remote))

    /**
     * 页面里的框架（iframe）能打开的地址。阅读器把书的每一章放在 iframe 里，地址是 blob: 开头的，
     * WebView 会把这种框架跳转也交到 shouldOverrideUrlLoading，拦掉的话 EPUB、TXT 这些书就一直停在“正在打开”，必须放行；
     * 还有 data:、about:（空白页、srcdoc）和书库自己的地址。别的一律拦下：书里的脚本不能把框架跳到外面的网站去
     */
    private fun frameAllowed(uri: Uri): Boolean = when (uri.scheme?.lowercase()) {
        "blob", "data", "about" -> true
        "http", "https" -> isOurs(originOf(uri.toString()))
        else -> false
    }

    private fun intercept(request: WebResourceRequest): WebResourceResponse? {
        val page = pageOrigin ?: return null
        val uri = request.url
        if (request.method != "GET" || (uri.scheme != "http" && uri.scheme != "https")) return null
        val o = originOf(uri.toString())
        if (!isOurs(o)) return null
        val path = uri.path ?: "/"
        if (path.startsWith("/api/") || path.startsWith("/offline-files/")) {
            // 已经下载到手机的书：直接用手机上的文件（联网时要和书库现在的版本一样）
            app.store.serveLocal(path, if (bridge.jsOffline) null else uri.getQueryParameter("v") ?: "")?.let { return it }
            // 断网：用手机上的书冒充书库
            if (bridge.jsOffline && o == page) return app.store.emulate(path, uri)
            return null
        }
        if (path.startsWith("/opds") || o != page) return null
        return app.webCache.serve(path)
    }

    // ---------- 网页渲染进程没了：换一个新的 WebView（App、后台听书都不受影响） ----------

    private fun recreateWeb(old: WebView) {
        val url = (try { old.url } catch (e: Exception) { null })?.takeIf { it.isNotEmpty() } ?: lastUrl
        lastUrl = null
        val now = SystemClock.elapsedRealtime()
        // 一分钟内又崩了：多半是同一本书太大，回到首页，免得一直崩
        val again = lastGone > 0 && now - lastGone < 60_000
        lastGone = now
        try { fileCallback?.onReceiveValue(null) } catch (e: Exception) {}
        fileCallback = null
        setReaderMode(false) // 新页面打开阅读器时会再告诉我们
        Events.detach(old)
        (old.parent as? ViewGroup)?.removeView(old)
        try { old.destroy() } catch (e: Exception) {}
        web = WebView(this)
        web.setBackgroundColor(bgColor)
        setContentView(web)
        setupWeb()
        val target = when {
            url.isNullOrEmpty() || url.startsWith("about:") || url.startsWith("data:") || url.startsWith("chrome-error:") -> ""
            again -> pageOrigin?.let { "$it/" } ?: ""
            else -> url
        }
        if (started) {
            reopen(target)
            toast(if (again) "网页又出问题了（多半是这本书太大、内存不够），先回到首页" else "网页出了问题（多半是内存不够），已经重新打开")
        } else reopenUrl = target
    }

    /** 重新打开网页；没有地址就从头连接书库 */
    private fun reopen(url: String) {
        if (url.isEmpty()) start() else web.loadUrl(url)
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (requestCode == REQ_FILE) {
            val uris = WebChromeClient.FileChooserParams.parseResult(resultCode, data)
                ?: data?.clipData?.let { c -> Array(c.itemCount) { c.getItemAt(it).uri } }
            fileCallback?.onReceiveValue(uris)
            fileCallback = null
            return
        }
        super.onActivityResult(requestCode, resultCode, data)
    }

    // ---------- 返回键、音量键 ----------

    private fun goBack() {
        web.evaluateJavascript("window.shukuBack ? window.shukuBack() : false") { r ->
            if (r != "true") {
                if (web.url?.startsWith("file:") == true && web.canGoBack()) web.goBack() else moveTaskToBack(true)
            }
        }
    }

    @Deprecated("Deprecated in Java")
    override fun onBackPressed() = goBack()

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        val vol = event.keyCode == KeyEvent.KEYCODE_VOLUME_DOWN || event.keyCode == KeyEvent.KEYCODE_VOLUME_UP
        // 阅读时音量键翻页；听书时音量键还是调音量
        if (vol && readerMode && app.config.bool("volume_keys", true) && NarrationService.instance == null) {
            if (event.action == KeyEvent.ACTION_DOWN) {
                Events.emit("key", JSONObject().put("dir", if (event.keyCode == KeyEvent.KEYCODE_VOLUME_DOWN) "next" else "prev"))
            }
            return true
        }
        return super.dispatchKeyEvent(event)
    }

    fun setReaderMode(on: Boolean) {
        readerMode = on
        refreshKeepScreen()
    }

    fun refreshKeepScreen() {
        if (readerMode && app.config.bool("keep_screen_on", true)) window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        else window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
    }

    // ---------- 状态栏、导航栏颜色跟着网页 ----------

    @Suppress("DEPRECATION")
    fun applyBars(color: String, dark: Boolean) {
        val c = try { Color.parseColor(color) } catch (e: Exception) { return }
        bgColor = c
        window.statusBarColor = c
        window.navigationBarColor = c
        if (::web.isInitialized) web.setBackgroundColor(c)
        if (Build.VERSION.SDK_INT >= 30) {
            val mask = WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS
            window.insetsController?.setSystemBarsAppearance(if (dark) 0 else mask, mask)
        } else {
            val light = View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR or View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
            val flags = window.decorView.systemUiVisibility
            window.decorView.systemUiVisibility = if (dark) flags and light.inv() else flags or light
        }
    }

    // ---------- 扫码配对：相机扫网页上的二维码会打开 shuku://pair?... ----------

    private fun handlePairIntent(intent: Intent?): Boolean {
        val uri = intent?.data ?: return false
        if (uri.scheme != "shuku" || uri.host != "pair") return false
        handlePairUri(uri)
        return true
    }

    private fun handlePairUri(uri: Uri) {
        val code = uri.getQueryParameter("c") ?: return
        val cands = listOfNotNull(uri.getQueryParameter("l"), uri.getQueryParameter("r"), uri.getQueryParameter("o"))
            .map { Config.norm(it) }.filter { it.isNotEmpty() }.distinct()
        toast("正在连接书库…")
        app.io.execute {
            var err: String? = "连不上书库。请确认手机和书库在同一个网络，或者已经打开 Tailscale"
            for (u in cands) {
                if (app.link.ping(u, 3000) != null) {
                    err = app.link.connect(u, code, "", deviceName())
                    break
                }
            }
            runOnUiThread {
                if (err == null) {
                    toast("已连接到书库")
                    getSharedPreferences("webstore", 0).edit().putString("shuku.token", app.config.token).apply()
                    start()
                } else {
                    toast(err ?: "")
                    if (!app.config.configured) start()
                }
            }
        }
    }

    // ---------- 其他 ----------

    fun askNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIFY)
        }
    }

    /** 列出手机自带语音的音色用（第一次调用时初始化，下一次才有结果） */
    fun deviceTts(): TextToSpeech? {
        if (voicesTts == null) runOnUiThread {
            if (voicesTts == null) voicesTts = TextToSpeech(applicationContext) { voicesReady = it == TextToSpeech.SUCCESS }
        }
        return if (voicesReady) voicesTts else null
    }

    fun toast(msg: String) = Toast.makeText(this, msg, Toast.LENGTH_LONG).show()

    companion object {
        private const val REQ_FILE = 11
        private const val REQ_NOTIFY = 12

        /** 书库 App 的界面在前台（看得见）。只在主线程改 */
        @Volatile private var visibleCount = 0
        val visible: Boolean get() = visibleCount > 0

        fun originOf(url: String): String {
            if (url.isEmpty()) return ""
            val u = Uri.parse(url)
            return if (u.scheme == null || u.authority == null) "" else "${u.scheme}://${u.authority}"
        }

        fun deviceName(): String = "安卓 · " + (Build.MODEL ?: "手机")
    }
}
