package app.shuku.core

import android.content.Context
import android.content.SharedPreferences
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.Callable
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** 连接信息：内网地址、Tailscale 地址、服务器 ID、登录令牌（手机和手表共用这套代码） */
class Config(context: Context) {
    val prefs: SharedPreferences = context.getSharedPreferences("shuku", Context.MODE_PRIVATE)

    var lan: String
        get() = prefs.getString("lan", "") ?: ""
        set(v) = prefs.edit().putString("lan", norm(v)).apply()
    var remote: String
        get() = prefs.getString("remote", "") ?: ""
        set(v) = prefs.edit().putString("remote", norm(v)).apply()
    var serverId: String
        get() = prefs.getString("server_id", "") ?: ""
        set(v) = prefs.edit().putString("server_id", v).apply()
    var name: String
        get() = prefs.getString("name", "") ?: ""
        set(v) = prefs.edit().putString("name", v).apply()
    var token: String
        get() = prefs.getString("token", "") ?: ""
        set(v) = prefs.edit().putString("token", v).apply()
    var lastBase: String
        get() = prefs.getString("last_base", "") ?: ""
        set(v) = prefs.edit().putString("last_base", norm(v)).apply()

    val configured: Boolean get() = token.isNotEmpty() && (lan.isNotEmpty() || remote.isNotEmpty() || lastBase.isNotEmpty())

    fun bool(key: String, def: Boolean) = prefs.getBoolean(key, def)
    fun str(key: String, def: String) = prefs.getString(key, def) ?: def

    /** 从配对、登录的结果里保存连接信息 */
    fun saveFrom(info: JSONObject, usedBase: String) {
        val e = prefs.edit()
        e.putString("token", info.optString("token", token))
        e.putString("lan", norm(info.optString("lan_url")))
        e.putString("remote", norm(info.optString("remote_url")))
        if (info.optString("id").isNotEmpty()) e.putString("server_id", info.optString("id"))
        if (info.optString("name").isNotEmpty()) e.putString("name", info.optString("name"))
        // 经手机转发的地址（127.0.0.1 上的端口，每次启动都会变）不能存
        val viaRelay = isRelay(usedBase)
        if (!viaRelay) e.putString("last_base", norm(usedBase))
        e.apply()
        // 服务器没填内网地址时，把这次用的地址记成内网地址
        if (!viaRelay && lan.isEmpty() && remote.isEmpty()) lan = usedBase
    }

    fun clear() {
        prefs.edit().remove("token").remove("last_base").apply()
    }

    companion object {
        /** 手表本机的转发地址（见 wear 的 PhoneRelay） */
        fun isRelay(u: String?): Boolean = (u ?: "").startsWith("http://127.0.0.1:")

        fun norm(u: String?): String {
            var s = (u ?: "").trim().trimEnd('/')
            if (s.isNotEmpty() && !s.startsWith("http://") && !s.startsWith("https://")) s = "http://$s"
            return s
        }
    }
}

/**
 * 内网 / Tailscale 自动切换：同时探测两个地址的 /api/ping，内网通就走内网，不通走 Tailscale。
 * 两个地址必须是同一台服务器（核对服务器 ID）。
 */
class ServerLink(private val cfg: Config, private val lanTimeout: Int = 1500, private val remoteTimeout: Int = 4500) {
    @Volatile var base: String? = null
        private set
    @Volatile var route: String = "offline"
        private set
    private val pool = Executors.newCachedThreadPool()

    val online: Boolean get() = base != null

    fun ping(url: String, timeoutMs: Int): JSONObject? = try {
        val r = Http.request("$url/api/ping", timeoutMs = timeoutMs)
        if (r.ok) JSONObject(r.text).takeIf { it.optString("app") == "shuku" } else null
    } catch (e: Exception) {
        null
    }

    /** 重新选线路，返回可用的地址（都不通返回 null） */
    @Synchronized
    fun probe(): String? {
        val cands = mutableListOf<Pair<String, String>>()
        if (cfg.lan.isNotEmpty()) cands += "lan" to cfg.lan
        if (cfg.remote.isNotEmpty() && cfg.remote != cfg.lan) cands += "remote" to cfg.remote
        if (cands.isEmpty() && cfg.lastBase.isNotEmpty()) cands += "lan" to cfg.lastBase
        val futures = cands.map { (r, u) -> Triple(r, u, pool.submit(Callable { ping(u, if (r == "lan") lanTimeout else remoteTimeout) })) }
        for ((r, u, f) in futures) {
            val wait = (if (r == "lan") lanTimeout else remoteTimeout) + 1000L
            val res = try { f.get(wait, TimeUnit.MILLISECONDS) } catch (e: Exception) { null } ?: continue
            val id = res.optString("id")
            if (cfg.serverId.isNotEmpty() && id.isNotEmpty() && id != cfg.serverId) continue
            if (cfg.serverId.isEmpty() && id.isNotEmpty()) cfg.serverId = id
            base = u
            route = r
            return u
        }
        base = null
        route = "offline"
        return null
    }

    /** JS 那边选好的线路也同步过来，免得两边不一致 */
    fun adopt(route: String, url: String?) {
        if (route == "offline" || url.isNullOrEmpty()) {
            this.base = null
            this.route = "offline"
        } else {
            this.base = Config.norm(url)
            this.route = route
        }
    }

    fun url(path: String): String = (base ?: throw IOException("连不上书库")) + path

    fun get(path: String, timeoutMs: Int = 15000): JSONObject = Http.getJson(url(path), cfg.token, timeoutMs)
    fun getArray(path: String, timeoutMs: Int = 15000) = Http.getArray(url(path), cfg.token, timeoutMs)
    fun send(method: String, path: String, json: JSONObject, timeoutMs: Int = 20000): HttpResult = Http.postJson(url(path), cfg.token, json, method, timeoutMs)

    /**
     * 用配对码或密码连接一台书库，成功后保存连接信息。address 是用户填的地址（内网或 Tailscale 都行）。
     * 返回 null 表示成功，否则是错误说明。
     */
    fun connect(address: String, code: String, password: String, device: String): String? {
        val base = Config.norm(address)
        if (base.isEmpty()) return "请填写书库地址"
        val ping = ping(base, 6000) ?: return "连不上 $base，请检查地址，确认手机和书库在同一个网络（或已开启 Tailscale）"
        return try {
            val r = if (code.isNotBlank()) {
                Http.postJson("$base/api/pair/claim", null, JSONObject().put("code", code.trim()).put("device", device))
            } else {
                Http.postJson("$base/api/auth/login", null, JSONObject().put("Password", password).put("Device", device))
            }
            if (!r.ok) return r.errorMessage()
            val j = JSONObject(r.text)
            if (!j.has("lan_url")) {
                // 密码登录只返回令牌，再取一次服务器信息
                val info = Http.getJson("$base/api/info", j.optString("token"))
                info.put("token", j.optString("token"))
                cfg.saveFrom(info, base)
            } else cfg.saveFrom(j, base)
            if (cfg.serverId.isEmpty()) cfg.serverId = ping.optString("id")
            this.base = base
            this.route = if (Config.isRelay(base)) "relay" else if (base == cfg.remote) "remote" else "lan"
            null
        } catch (e: Exception) {
            "连接失败：${e.message ?: e.javaClass.simpleName}"
        }
    }
}
