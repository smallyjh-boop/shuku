package app.shuku

import android.content.Intent
import android.net.Uri
import android.webkit.JavascriptInterface
import org.json.JSONArray
import org.json.JSONObject
import java.util.Locale

/**
 * 网页里的 window.ShukuNative。方法都在 WebView 的后台线程里调用，要动界面的转到主线程。
 * 网页端对应的代码在 web/js/native.js。
 */
class Bridge(private val activity: MainActivity) {
    private val app get() = ShukuApp.app
    private val store = app.getSharedPreferences("webstore", 0)

    @Volatile var jsOffline = false
        private set

    @JavascriptInterface fun version(): String = BuildConfig.VERSION_NAME

    // ---------- 在线升级 ----------

    @JavascriptInterface fun appInfo(): String = JSONObject().put("versionName", BuildConfig.VERSION_NAME).put("versionCode", BuildConfig.VERSION_CODE).toString()

    /** “设置 → 版本与升级”里的“更新” */
    @JavascriptInterface fun updateApp() = activity.runOnUiThread { Updater.checkNow(activity) }

    /** “更新手表”：下载手表安装包，交给 Wear OS 工具箱 */
    @JavascriptInterface fun updateWatchApp() = activity.runOnUiThread { Updater.updateWatch(activity) }

    @JavascriptInterface fun deviceName(): String = MainActivity.deviceName()

    // ---------- 存储（替代 localStorage，内网、Tailscale 两个地址共用） ----------

    @JavascriptInterface fun storageGet(key: String): String? = store.getString(key, null)
    @JavascriptInterface fun storageSet(key: String, value: String) { store.edit().putString(key, value).apply() }
    @JavascriptInterface fun storageRemove(key: String) { store.edit().remove(key).apply() }
    @JavascriptInterface fun storageKeys(): String = JSONArray(store.all.keys.toList()).toString()

    // ---------- 线路、登录 ----------

    @JavascriptInterface fun setRoute(json: String) {
        val j = JSONObject(json)
        val route = j.optString("route")
        jsOffline = route == "offline"
        app.link.adopt(route, j.optString("base"))
        if (!jsOffline) app.io.execute { app.progress.flush(); app.webCache.precache() }
    }

    @JavascriptInterface fun setNetwork(json: String) {
        val j = JSONObject(json)
        if (j.has("lan")) app.config.lan = j.optString("lan")
        if (j.has("remote")) app.config.remote = j.optString("remote")
        if (j.optString("serverId").isNotEmpty()) app.config.serverId = j.optString("serverId")
    }

    @JavascriptInterface fun setToken(token: String) {
        if (token.isNotEmpty()) app.config.token = token
    }

    // ---------- 外观、阅读 ----------

    @JavascriptInterface fun setThemeColor(color: String, dark: Boolean) = activity.runOnUiThread { activity.applyBars(color, dark) }

    @JavascriptInterface fun setReaderMode(on: Boolean) = activity.runOnUiThread { activity.setReaderMode(on) }

    @JavascriptInterface fun saveProgress(bookId: Int, body: String) = app.progress.save(bookId, body)

    // ---------- App 设置 ----------

    /** 是不是墨水屏阅读器（网页据此自动打开墨水屏模式） */
    @JavascriptInterface fun isEInk(): Boolean = EInk.detected

    /** 设备信息（屏幕实际尺寸、网页组件版本）：墨水屏模式按屏幕尺寸定字号；设置页“手机 App”里显示，排查兼容问题用 */
    @JavascriptInterface fun deviceInfo(): String = EInk.info(activity).toString()

    /**
     * download_limit_mb：下载到手机的书最多占多少 MB（0 表示不限），超过时先删最早下载、已经读完的书；
     * auto_clean_finished：App 启动连上书库时，把下载以后读完了的书从手机上删掉
     */
    @JavascriptInterface fun prefs(): String = JSONObject()
        .put("volume_keys", app.config.bool("volume_keys", true))
        .put("keep_screen_on", app.config.bool("keep_screen_on", true))
        .put("auto_download", app.config.bool("auto_download", false))
        .put("tts_engine", app.config.str("tts_engine", "server"))
        .put("download_limit_mb", app.downloads.limitMb())
        .put("auto_clean_finished", app.config.bool("auto_clean_finished", false))
        .toString()

    @JavascriptInterface fun setPref(key: String, value: String) {
        val e = app.config.prefs.edit()
        when (key) {
            "volume_keys", "keep_screen_on", "auto_download", "auto_clean_finished" -> e.putBoolean(key, value == "true")
            "tts_engine" -> e.putString(key, if (value == "device") "device" else "server")
            "download_limit_mb" -> e.putInt(key, (value.trim().toDoubleOrNull() ?: 0.0).toInt().coerceIn(0, 100_000_000))
        }
        e.apply()
        if (key == "auto_download" && value == "true") app.io.execute { app.downloads.autoDownload() }
        if (key == "auto_clean_finished" && value == "true") app.io.execute { app.downloads.autoClean() }
        activity.runOnUiThread { activity.refreshKeepScreen() }
    }

    // ---------- 下载到手机 ----------

    /** 下载到手机的书（数组，每本带 size 字节）。旧版网页用这个 */
    @JavascriptInterface fun downloads(): String = app.downloads.list().toString()

    /** 新版网页用：{"items": 同 downloads(), "total_size": 一共占多少字节, "limit_mb": 下载上限，0 表示不限} */
    @JavascriptInterface fun downloadInfo(): String = app.downloads.info().toString()

    @JavascriptInterface fun download(bookId: Int, json: String) {
        app.downloads.start(bookId, try { JSONObject(json) } catch (e: Exception) { null })
    }

    @JavascriptInterface fun removeDownload(bookId: Int) = app.downloads.remove(bookId)

    // ---------- 听书 ----------

    @JavascriptInterface fun tts(cmd: String, arg: String) {
        if (cmd == "start") activity.runOnUiThread { activity.askNotificationPermission() }
        NarrationService.command(activity.applicationContext, cmd, arg)
    }

    @JavascriptInterface fun ttsState(): String = NarrationService.state().toString()

    /** 手机自带语音里的中文、英文音色 */
    @JavascriptInterface fun ttsVoices(): String {
        val out = JSONArray()
        val t = activity.deviceTts() ?: return out.toString()
        try {
            t.voices?.filter { it.locale.language in setOf("zh", "en", "ja") && !it.isNetworkConnectionRequired }
                ?.sortedBy { it.locale.toString() + it.name }
                ?.forEach { v -> out.put(JSONObject().put("id", v.name).put("name", "${v.locale.getDisplayName(Locale.CHINA)} · ${v.name.substringAfterLast('-')}")) }
        } catch (e: Exception) {}
        return out.toString()
    }

    // ---------- 手表、账户、其他 ----------

    /** 把连接信息发给手表：要联网、等蓝牙，放到后台做，结果用 watch 事件告诉网页 */
    @JavascriptInterface fun syncWatch() {
        val ctx = activity.applicationContext
        ShukuApp.app.io.execute { Events.emit("watch", WearSync.sendConfig(ctx)) }
    }

    @JavascriptInterface fun logout() = activity.runOnUiThread { activity.logout() }

    @JavascriptInterface fun openExternal(url: String) = activity.runOnUiThread {
        try { activity.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) } catch (e: Exception) {}
    }

    /** 连接页上的“重试” */
    @JavascriptInterface fun retry() = activity.runOnUiThread { activity.start() }

    @JavascriptInterface fun configured(): Boolean = app.config.configured

    /** 首次设置页面：用配对码或密码连接书库 */
    @JavascriptInterface fun connect(address: String, code: String, password: String): String {
        val err = app.link.connect(address, code, password, deviceName())
        if (err == null) activity.runOnUiThread { activity.start() }
        return JSONObject().put("ok", err == null).put("error", err ?: "").toString()
    }
}
