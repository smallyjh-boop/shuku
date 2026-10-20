package app.shuku

import android.content.Context
import android.util.Base64
import app.shuku.core.Http
import app.shuku.core.Relay
import com.google.android.gms.tasks.Tasks
import com.google.android.gms.wearable.MessageEvent
import com.google.android.gms.wearable.Wearable
import com.google.android.gms.wearable.WearableListenerService
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit

/**
 * 和 Wear OS 手表通信（Google 的 Wearable 数据层，手机和手表的 App 包名、签名必须一样）：
 *   /shuku/config         手机 → 手表：书库地址 + 一次性配对码，手表用它换自己的登录令牌
 *   /shuku/config/request 手表 → 手机：请手机发连接信息
 *   /shuku/config/error   手机 → 手表：发不了连接信息的原因（手机还没连书库、连不上书库）
 *   /shuku/tts/state      手机 → 手表：听书状态；手表 → 手机：请发一次状态
 *   /shuku/tts/<命令>      手表 → 手机：pause / resume / next / prev / stop / volup / voldown
 */
object WearSync {
    const val PATH_CONFIG = "/shuku/config"
    const val PATH_REQUEST = "/shuku/config/request"
    const val PATH_ERROR = "/shuku/config/error"
    const val PATH_STATE = "/shuku/tts/state"

    @Volatile private var lastPush = 0L
    @Volatile private var hasWatch: Boolean? = null

    /** 把连接信息发给所有连着的手表，返回给网页显示的结果 */
    fun sendConfig(ctx: Context): JSONObject {
        val app = ShukuApp.app
        if (!app.config.configured) return JSONObject().put("ok", false).put("message", "手机上的书库 App 还没有连接书库，请先在手机上连好")
        if (!app.link.online && app.link.probe() == null) return JSONObject().put("ok", false).put("message", "现在连不上书库，联网后再连接手表")
        return try {
            val nodes = Tasks.await(Wearable.getNodeClient(ctx).connectedNodes, 6, TimeUnit.SECONDS)
            hasWatch = nodes.isNotEmpty()
            if (nodes.isEmpty()) return JSONObject().put("ok", false).put("message", "没有找到连着的手表。请确认手表已经和这台手机配对，并且装好了书库手表版")
            var sent = 0
            for (n in nodes) {
                // 每块手表一个配对码，手表在设备列表里单独显示，可以单独移除
                val r = app.link.send("POST", "/api/pair", JSONObject())
                if (!r.ok) throw IOException(r.errorMessage())
                val p = JSONObject(r.text)
                val msg = JSONObject().put("code", p.optString("code"))
                    .put("lan", p.optString("lan_url").ifEmpty { app.config.lan })
                    .put("remote", p.optString("remote_url").ifEmpty { app.config.remote })
                    .put("origin", app.link.base ?: "")
                    .put("id", app.config.serverId).put("name", app.config.name)
                try {
                    Tasks.await(Wearable.getMessageClient(ctx).sendMessage(n.id, PATH_CONFIG, msg.toString().toByteArray()), 6, TimeUnit.SECONDS)
                    sent++
                } catch (e: Exception) {}
            }
            if (sent == 0) JSONObject().put("ok", false).put("message", "发送失败，请在手表上打开书库后再试")
            else JSONObject().put("ok", true).put("message", "已把连接信息发给 $sent 块手表")
        } catch (e: Exception) {
            JSONObject().put("ok", false).put("message", "连接手表失败：${e.message ?: "这台手机没有 Google Play 服务"}")
        }
    }

    /** 手表来要连接信息、但手机这边办不到时，告诉手表原因 */
    fun sendError(ctx: Context, node: String, message: String) {
        try {
            Tasks.await(Wearable.getMessageClient(ctx).sendMessage(node, PATH_ERROR, message.toByteArray()), 6, TimeUnit.SECONDS)
        } catch (e: Exception) {}
    }

    /** 听书状态推给手表（最多一秒一次） */
    fun pushState(ctx: Context, state: JSONObject, force: Boolean = false) {
        val now = System.currentTimeMillis()
        if (!force && (now - lastPush < 1000 || hasWatch == false)) return
        lastPush = now
        val data = state.toString().toByteArray()
        ShukuApp.app.io.execute {
            try {
                val nodes = Tasks.await(Wearable.getNodeClient(ctx).connectedNodes, 3, TimeUnit.SECONDS)
                hasWatch = nodes.isNotEmpty()
                for (n in nodes) Wearable.getMessageClient(ctx).sendMessage(n.id, PATH_STATE, data)
            } catch (e: Exception) {
                hasWatch = false
            }
        }
    }
}

/** 收手表发来的消息 */
class PhoneWearService : WearableListenerService() {
    override fun onMessageReceived(event: MessageEvent) {
        val path = event.path
        when {
            path == WearSync.PATH_REQUEST -> {
                val r = WearSync.sendConfig(this)
                if (!r.optBoolean("ok")) WearSync.sendError(this, event.sourceNodeId, r.optString("message"))
            }
            path == WearSync.PATH_STATE -> WearSync.pushState(this, NarrationService.state(), force = true)
            path == Relay.PATH_REQ -> RelayServer.serve(this, event.sourceNodeId, event.data)
            path.startsWith("/shuku/tts/") -> {
                val cmd = path.removePrefix("/shuku/tts/")
                if (cmd in setOf("pause", "resume", "next", "prev", "stop")) NarrationService.command(this, cmd, "")
                // 手表上调手机的音量（显示系统的音量条）
                if (cmd == "volup" || cmd == "voldown") (getSystemService(AUDIO_SERVICE) as android.media.AudioManager).adjustStreamVolume(
                    android.media.AudioManager.STREAM_MUSIC,
                    if (cmd == "volup") android.media.AudioManager.ADJUST_RAISE else android.media.AudioManager.ADJUST_LOWER,
                    android.media.AudioManager.FLAG_SHOW_UI)
            }
        }
    }
}

/**
 * 手表连不上书库时，手机代为转发（手表那边见 wear 的 PhoneRelay）。
 * 只转发书库的 /api/ 接口，用手表自己的登录令牌（请求头原样带过来），手机不另外保存令牌。
 */
object RelayServer {
    private class Reply(val status: Int, val type: String, val body: ByteArray)

    fun serve(ctx: Context, node: String, payload: ByteArray) {
        val req = try {
            org.json.JSONObject(String(payload, Charsets.UTF_8))
        } catch (e: Exception) {
            return
        }
        val id = req.optString("id")
        if (id.isEmpty()) return
        val app = ShukuApp.app
        app.io.execute {
            val r = try {
                forward(app, req)
            } catch (e: Exception) {
                err(502, "转发失败：${e.message ?: e.javaClass.simpleName}")
            }
            sendReply(ctx, node, id, r)
        }
    }

    private fun forward(app: ShukuApp, req: org.json.JSONObject): Reply {
        val path = req.optString("p")
        if (!path.startsWith("/api/")) return err(403, "不转发这个地址")
        if (!app.link.online && app.link.probe() == null) return err(503, "手机连不上书库")
        val base = app.link.base ?: return err(503, "手机连不上书库")
        val method = req.optString("m", "GET")
        val h = req.optJSONObject("h")
        val auth = h?.optString("Authorization").orEmpty()
        val ct = h?.optString("Content-Type").orEmpty().ifEmpty { null }
        val b64 = req.optString("b")
        val body = if (b64.isEmpty()) null else Base64.decode(b64, Base64.DEFAULT)
        val headers = if (auth.isNotEmpty()) mapOf("Authorization" to auth) else emptyMap()
        // 听书要合成语音，比普通请求等得久一些
        val timeout = if (method == "GET") 20000 else 60000
        val r = Http.request("$base$path", method, null, body, ct, timeout, headers)
        return Reply(r.code, r.header("Content-Type") ?: "application/octet-stream", r.body)
    }

    private fun sendReply(ctx: Context, node: String, id: String, r: Reply) {
        val total = if (r.body.isEmpty()) 1 else (r.body.size + Relay.CHUNK - 1) / Relay.CHUNK
        for (i in 0 until total) {
            val from = i * Relay.CHUNK
            val to = minOf(r.body.size, from + Relay.CHUNK)
            val data = if (r.body.isEmpty()) ByteArray(0) else r.body.copyOfRange(from, to)
            val msg = Relay.encodeChunk(id, i, total, r.status, r.type, data)
            try {
                Tasks.await(Wearable.getMessageClient(ctx).sendMessage(node, Relay.PATH_RES, msg), 20, TimeUnit.SECONDS)
            } catch (e: Exception) {
                return // 手表已经断开：不用再发了，它会超时重试
            }
        }
    }

    private fun err(status: Int, msg: String) =
        Reply(status, "application/json; charset=utf-8", org.json.JSONObject().put("error", msg).toString().toByteArray())
}
