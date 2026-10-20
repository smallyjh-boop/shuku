package app.shuku.wear

import android.content.Intent
import app.shuku.core.Config
import app.shuku.core.Http
import app.shuku.core.Relay
import com.google.android.gms.wearable.MessageEvent
import com.google.android.gms.wearable.WearableListenerService
import org.json.JSONObject

/**
 * 收手机发来的消息：
 *   /shuku/config     书库地址和一次性配对码 → 手表自己去换登录令牌
 *   /shuku/config/error 手机发不了连接信息的原因
 *   /shuku/tts/state  手机上听书的状态
 */
class WatchListener : WearableListenerService() {
    override fun onMessageReceived(event: MessageEvent) {
        val app = WearApp.app
        when (event.path) {
            "/shuku/config" -> {
                val j = JSONObject(String(event.data))
                val code = j.optString("code")
                val oldToken = app.config.token
                // 手机发来的地址都连不上（比如手表在外面）时，最后经手机转发（这条消息就是手机发来的，手机一定连着）
                val cands = listOf(j.optString("lan"), j.optString("origin"), j.optString("remote")).map { Config.norm(it) }.filter { it.isNotEmpty() }.distinct() + PhoneRelay.start()
                var err: String? = "手表连不上书库（${cands.size - 1} 个地址），也没能经手机转发，请确认手机能连上书库"
                for (u in cands) {
                    if (app.link.ping(u, 6000) == null) continue
                    err = app.link.connect(u, code, "", WearApp.DEVICE)
                    if (err == null) break
                }
                if (err == null) {
                    if (j.optString("lan").isNotEmpty()) app.config.lan = j.optString("lan")
                    if (j.optString("remote").isNotEmpty()) app.config.remote = j.optString("remote")
                    // 重新连接过：把旧的登录注销掉，书库的设备列表里不留重复的手表
                    val base = app.link.base
                    if (oldToken.isNotEmpty() && oldToken != app.config.token && base != null) {
                        runCatching { Http.postJson("$base/api/auth/logout", oldToken, JSONObject(), timeoutMs = 6000) }
                    }
                }
                app.config.prefs.edit().putString("connect_error", err ?: "").apply()
                sendBroadcast(Intent(WearApp.ACTION_CONFIGURED).setPackage(packageName))
                if (err == null) Today.updateViews() // Tile、表盘上不再显示“还没连接书库”
            }
            Relay.PATH_RES -> PhoneRelay.onChunk(event.data)
            "/shuku/config/error" -> {
                app.config.prefs.edit().putString("connect_error", String(event.data)).apply()
                sendBroadcast(Intent(WearApp.ACTION_CONFIGURED).setPackage(packageName))
            }
            "/shuku/tts/state" -> {
                // 和上次一样（比如首页问了一下、手机那边没变化）：不写、不通知，省电
                val s = String(event.data)
                if (s == app.config.str("tts_state", "")) return
                app.config.prefs.edit().putString("tts_state", s).putLong("tts_at", System.currentTimeMillis()).apply()
                sendBroadcast(Intent(WearApp.ACTION_TTS).setPackage(packageName))
            }
        }
    }
}
