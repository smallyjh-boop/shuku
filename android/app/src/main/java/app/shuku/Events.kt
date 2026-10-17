package app.shuku

import android.webkit.WebView
import org.json.JSONObject
import java.lang.ref.WeakReference

/** 原生代码 → 网页：调用 window.shukuNativeEvent(类型, JSON) */
object Events {
    private var webRef: WeakReference<WebView>? = null

    fun attach(web: WebView) { webRef = WeakReference(web) }
    fun detach(web: WebView) { if (webRef?.get() === web) webRef = null }
    val attached: Boolean get() = webRef?.get() != null

    fun emit(type: String, json: String) {
        val web = webRef?.get() ?: return
        val js = "window.shukuNativeEvent && window.shukuNativeEvent(${JSONObject.quote(type)}, ${JSONObject.quote(json)})"
        ShukuApp.app.main.post { web.evaluateJavascript(js, null) }
    }

    fun emit(type: String, obj: JSONObject) = emit(type, obj.toString())
}
