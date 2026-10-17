package app.shuku.wear

import android.app.Application
import android.content.Context
import android.os.Handler
import android.os.Looper
import app.shuku.core.Config
import app.shuku.core.Http
import app.shuku.core.HttpResult
import app.shuku.core.ServerLink
import com.google.android.gms.tasks.Tasks
import com.google.android.gms.wearable.Wearable
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class WearApp : Application() {
    lateinit var config: Config
    lateinit var link: ServerLink
    val io = Executors.newFixedThreadPool(3)
    val main = Handler(Looper.getMainLooper())

    override fun onCreate() {
        super.onCreate()
        app = this
        Http.userAgent = "ShukuWatch/${BuildConfig.VERSION_NAME}"
        config = Config(this)
        // 手表常常通过手机的蓝牙转发上网，探测时多等一会儿
        link = ServerLink(config, lanTimeout = 3500, remoteTimeout = 7000)
    }

    /** 令牌失效：这块手表在书库的“设备”里被移除了 */
    class Unauthorized : IOException("这块手表已经从书库里移除了，请重新连接")

    /**
     * 调书库接口（在后台线程调用）。还没选线路就先探测；请求失败时重选一次线路再试（比如刚出门，内网不通了）。
     */
    fun api(method: String, path: String, body: JSONObject? = null, timeoutMs: Int = 15000): HttpResult {
        if (!link.online && link.probe() == null) throw IOException(OFFLINE)
        fun once(): HttpResult = if (body == null) Http.request(link.url(path), method, config.token, timeoutMs = timeoutMs)
        else Http.postJson(link.url(path), config.token, body, method, timeoutMs)
        val r = try {
            once()
        } catch (e: IOException) {
            if (link.probe() == null) throw IOException(OFFLINE)
            once()
        }
        if (r.code == 401) {
            config.clear()
            config.prefs.edit().putString("connect_error", Unauthorized().message).apply()
            throw Unauthorized()
        }
        return r
    }

    fun json(path: String, timeoutMs: Int = 15000): JSONObject {
        val r = api("GET", path, timeoutMs = timeoutMs)
        if (!r.ok) throw IOException(r.errorMessage())
        return JSONObject(r.text)
    }

    /**
     * 上传阅读进度。连不上时先记下来，下次联网再补传；补传前比一下时间，书库上更新过（在别处读过）就不覆盖。
     */
    fun saveProgress(bookId: Long, body: JSONObject) {
        body.put("at", System.currentTimeMillis() / 1000)
        if (putProgress(bookId, body)) {
            config.prefs.edit().remove("pending_$bookId").apply()
            return
        }
        val old = config.prefs.getString("pending_$bookId", null)?.let { runCatching { JSONObject(it) }.getOrNull() }
        if (old != null) body.put("seconds", minOf(600, old.optInt("seconds") + body.optInt("seconds")))
        config.prefs.edit().putString("pending_$bookId", body.toString()).apply()
    }

    private fun putProgress(bookId: Long, body: JSONObject): Boolean = try {
        api("PUT", "/api/books/$bookId/progress", body, timeoutMs = 8000).ok
    } catch (e: Unauthorized) {
        true // 没法再传了，不要一直留着
    } catch (e: Exception) {
        false
    }

    /** 补传离线时记下的进度 */
    fun flushPending() {
        val keys = config.prefs.all.keys.filter { it.startsWith("pending_") }
        for (k in keys) {
            val id = k.removePrefix("pending_").toLongOrNull() ?: continue
            val body = config.prefs.getString(k, null)?.let { runCatching { JSONObject(it) }.getOrNull() }
            if (body == null) {
                config.prefs.edit().remove(k).apply()
                continue
            }
            try {
                val cur = json("/api/books/$id/progress", 8000)
                if (cur.optLong("updated_at") > body.optLong("at")) {
                    // 之后在手机或网页上读过，保留那边的位置，只补上阅读时长
                    val keep = JSONObject().put("file_id", cur.optLong("file_id")).put("location", cur.optString("location"))
                        .put("percent", cur.optDouble("percent")).put("device", cur.optString("device")).put("seconds", body.optInt("seconds"))
                    if (putProgress(id, keep)) config.prefs.edit().remove(k).apply()
                } else if (putProgress(id, body)) config.prefs.edit().remove(k).apply()
            } catch (e: Exception) {
                return // 还是连不上，下次再说
            }
        }
    }

    companion object {
        lateinit var app: WearApp
            private set
        const val ACTION_CONFIGURED = "app.shuku.wear.CONFIGURED"
        const val ACTION_TTS = "app.shuku.wear.TTS"
        const val DEVICE = "Wear OS 手表"
        const val OFFLINE = "连不上书库（内网和 Tailscale 都不通）"
    }
}

/** 给手机发消息（在后台线程调用）：请手机发连接信息、控制手机上的听书。返回是否找到了手机 */
object PhoneLink {
    fun send(ctx: Context, path: String, data: ByteArray = ByteArray(0)): Boolean = try {
        val nodes = Tasks.await(Wearable.getNodeClient(ctx).connectedNodes, 6, TimeUnit.SECONDS)
        var sent = 0
        for (n in nodes) {
            try {
                Tasks.await(Wearable.getMessageClient(ctx).sendMessage(n.id, path, data), 6, TimeUnit.SECONDS)
                sent++
            } catch (e: Exception) {}
        }
        sent > 0
    } catch (e: Exception) {
        false
    }
}
