package app.shuku

import android.content.Context
import android.os.Build
import android.webkit.WebView
import org.json.JSONObject

/**
 * 墨水屏阅读器（墨案 inkPad、文石 BOOX、掌阅等）：网页那边自动打开墨水屏模式（黑白、没有动画、按页翻，字号按屏幕尺寸）。
 * 系统的网页组件（Android System WebView）太旧时，网页自己换成兼容旧系统的那一套（web/js/boot.js 判断），
 * App 这边只是预先缓存的时候把那一套也缓存上，离线也能打开。
 */
object EInk {
    private val marks = listOf("moaan", "inkpad", "onyx", "boox", "bigme", "meebook", "likebook", "hanvon", "pocketbook",
        "dasung", "boyue", "ireader", "hisense_e", "kindle", "kobo", "eink", "e-ink")

    val detected: Boolean by lazy {
        val s = listOf(Build.MANUFACTURER, Build.BRAND, Build.MODEL, Build.DEVICE, Build.PRODUCT, Build.HARDWARE, Build.BOARD, Build.DISPLAY)
            .joinToString(" ").lowercase()
        // 墨案的固件号形如 MAS_EPD101_…（EPD：电子纸屏）
        marks.any { s.contains(it) } || Regex("(^|[^a-z])epd").containsMatchIn(s) || Build.MODEL.equals("SR9", ignoreCase = true)
    }

    /** 低于这个版本的网页组件，网页会换成兼容旧系统的那一套（和 boot.js 的判断一致：CSS :has、color-mix 都要有） */
    const val MODERN_WEBVIEW = 111

    fun webViewVersion(): String = try { WebView.getCurrentWebViewPackage()?.versionName ?: "" } catch (e: Exception) { "" }

    fun webViewMajor(): Int = webViewVersion().substringBefore('.').toIntOrNull() ?: 0

    /** 网页会用兼容旧系统的那一套 */
    val legacyWeb: Boolean get() = webViewMajor().let { it in 1 until MODERN_WEBVIEW }

    /** 设备信息：屏幕的实际尺寸（英寸）给网页算字号；设置页显示，排查兼容问题也用得上 */
    fun info(ctx: Context): JSONObject {
        val m = ctx.resources.displayMetrics
        val wIn = m.widthPixels / m.xdpi.coerceAtLeast(1f)
        val hIn = m.heightPixels / m.ydpi.coerceAtLeast(1f)
        val diag = Math.sqrt((wIn * wIn + hIn * hIn).toDouble())
        return JSONObject()
            .put("brand", Build.BRAND).put("model", Build.MODEL).put("android", Build.VERSION.RELEASE)
            .put("webview", webViewVersion()).put("eink", detected)
            .put("abi", Build.SUPPORTED_ABIS.firstOrNull() ?: "")
            .put("screen", JSONObject()
                .put("w", m.widthPixels).put("h", m.heightPixels).put("density", m.density.toDouble())
                .put("dpi", m.densityDpi).put("inches", Math.round(diag * 10) / 10.0))
    }
}
