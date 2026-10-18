package app.shuku

import android.webkit.WebView
import org.json.JSONObject
import java.lang.ref.WeakReference

/** 原生代码 → 网页：调用 window.shukuNativeEvent(类型, JSON) */
object Events {
    @Volatile private var webRef: WeakReference<WebView>? = null

    /**
     * 每换一个网页（App 重新打开、网页崩溃后重建）就加一。后台听书靠它判断：
     * 给它句子的那个网页还在不在（不在了就没人给下一章，进度也要原生自己存）
     */
    @Volatile var generation = 0
        private set

    /** 只在主线程调用 */
    fun attach(web: WebView) {
        webRef = WeakReference(web)
        generation++
        NarrationService.webChanged()
    }

    fun detach(web: WebView) {
        if (webRef?.get() === web) webRef = null
        NarrationService.webChanged()
    }

    /** 同一个 WebView 打开了新页面（重新加载、换地址，不包括网页内 # 路由的变化）：原来的页面也没了。只在主线程调用 */
    fun pageStarted(web: WebView) {
        if (webRef?.get() !== web) return
        generation++
        NarrationService.webChanged()
    }

    val attached: Boolean get() = webRef?.get() != null

    fun emit(type: String, json: String) {
        val web = webRef?.get() ?: return
        val js = "window.shukuNativeEvent && window.shukuNativeEvent(${JSONObject.quote(type)}, ${JSONObject.quote(json)})"
        ShukuApp.app.main.post {
            // 网页可能刚被换掉（崩溃后重建）：已经销毁的 WebView 不能再用
            if (webRef?.get() === web) web.evaluateJavascript(js, null)
        }
    }

    fun emit(type: String, obj: JSONObject) = emit(type, obj.toString())
}
