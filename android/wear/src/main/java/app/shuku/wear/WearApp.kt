package app.shuku.wear

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import app.shuku.core.Config
import app.shuku.core.Http
import app.shuku.core.HttpResult
import app.shuku.core.ServerLink
import com.google.android.gms.tasks.Tasks
import com.google.android.gms.wearable.Wearable
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.Callable
import java.util.concurrent.ExecutorCompletionService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class WearApp : Application() {
    lateinit var config: Config
    lateinit var link: ServerLink
    val io = Executors.newFixedThreadPool(3)
    val main = Handler(Looper.getMainLooper())

    private val pingPool = Executors.newCachedThreadPool()
    private val probeLock = Any()
    private val progressLock = Any()
    @Volatile private var probedAt = 0L // 上次选完线路的时间（elapsedRealtime）
    @Volatile private var okAt = 0L // 上次请求书库成功的时间
    @Volatile private var offlineUntil = 0L // 刚探测过都不通：这之前直接按离线处理，不再一个个等超时
    private var defaultNet: Network? = null // 现在的默认网络（只在主线程用）

    override fun onCreate() {
        super.onCreate()
        app = this
        Http.userAgent = "ShukuWatch/${BuildConfig.VERSION_NAME}"
        // 连不上的地址几秒就放弃（手表经手机蓝牙转发、手机没开 Tailscale 时，不用等完整个请求的超时）
        Http.maxConnectMs = 5000
        config = Config(this)
        // 手表常常通过手机的蓝牙转发上网，探测时多等一会儿
        link = ServerLink(config, lanTimeout = 3500, remoteTimeout = 7000)
        // 听书的通知（播放控制）
        getSystemService(NotificationManager::class.java)?.createNotificationChannel(
            NotificationChannel(CH_LISTEN, "听书", NotificationManager.IMPORTANCE_LOW).apply { setShowBadge(false) })
        watchNetwork()
        // 手机在身边时，出门也能经手机连书库（见 PhoneRelay）
        PhoneRelay.start()
        PhoneRelay.refreshAsync(this)
        // 一周没听的离线听书包删掉
        io.execute { runCatching { ListenStore.expire(this, 7 * 24 * 3600_000L) } }
    }

    /** 令牌失效：这块手表在书库的“设备”里被移除了 */
    class Unauthorized : IOException("这块手表已经从书库里移除了，请重新连接")

    // ---------- 网络 ----------

    /** 手表现在有没有网（Wi-Fi、手机蓝牙转发、LTE 都算）。后台被系统限制联网时也算没有 */
    fun hasNetwork(): Boolean = hasIp() || PhoneRelay.phoneLikely()

    /** 手表自己有没有网络（Wi-Fi、LTE、蓝牙网络共享） */
    private fun hasIp(): Boolean = try {
        val cm = getSystemService(ConnectivityManager::class.java)
        val n = cm?.activeNetwork
        n != null && cm.getNetworkCapabilities(n)?.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) == true
    } catch (e: Exception) {
        true // 查不了就当有网，照常去连
    }

    /** 现在值得去连书库：有网，而且不是刚探测过都不通 */
    fun netUsable(): Boolean = hasNetwork() && SystemClock.elapsedRealtime() >= offlineUntil

    /** 用户点了“刷新”“重试”：马上重新连，不管刚才是不是不通 */
    fun retryNow() {
        offlineUntil = 0
        okAt = 0
    }

    // 网络换了（连上 Wi-Fi、蓝牙转发断开等）：重新选线路，听书重试没合成成功的句子
    private fun watchNetwork() {
        try {
            getSystemService(ConnectivityManager::class.java)?.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) {
                    if (network == defaultNet) return
                    defaultNet = network
                    netChanged()
                }

                override fun onLost(network: Network) {
                    if (network != defaultNet) return
                    defaultNet = null
                    netChanged()
                }
            }, main)
        } catch (e: Exception) {
        }
    }

    private fun netChanged() {
        offlineUntil = 0
        okAt = 0 // 下一个请求先重新选线路
        ListenService.instance?.onNetwork()
    }

    /**
     * 调书库接口（在后台线程调用）。手表没网直接按离线处理；还没选线路、或者好一阵没用过（可能已经出门了）就先探测；
     * 请求失败时重选一次线路再试（比如刚出门，内网不通了）。刚探测过都不通时直接按离线处理，不用每个请求都等一遍超时。
     */
    fun api(method: String, path: String, body: JSONObject? = null, timeoutMs: Int = 15000): HttpResult {
        if (!hasNetwork()) throw IOException(NO_NETWORK)
        val t0 = SystemClock.elapsedRealtime()
        var probed = false
        if (!link.online || t0 - okAt > STALE_MS) {
            probed = true
            if (!reprobe(t0)) throw IOException(OFFLINE)
        }
        fun once(): HttpResult = if (body == null) Http.request(link.url(path), method, config.token, timeoutMs = timeoutMs)
        else Http.postJson(link.url(path), config.token, body, method, timeoutMs)
        val r = try {
            once()
        } catch (e: IOException) {
            // 刚刚才选好的线路：不是线路的问题（比如书库处理得慢），不用再探测一遍
            if (probed) throw e
            if (!reprobe(t0)) throw IOException(OFFLINE)
            once()
        }
        okAt = SystemClock.elapsedRealtime()
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

    /** 重新选线路。since 之后别的请求刚选过，就直接用它的结果，不重复探测 */
    private fun reprobe(since: Long): Boolean {
        synchronized(probeLock) {
            if (probedAt > since) return link.online
            if (SystemClock.elapsedRealtime() < offlineUntil) return false
            val ok = probeRoutes() != null
            probedAt = SystemClock.elapsedRealtime()
            if (ok) okAt = probedAt else offlineUntil = probedAt + OFFLINE_HOLD_MS
            return ok
        }
    }

    /**
     * 同时探测内网和 Tailscale 的 /api/ping：内网通就走内网；Tailscale 先通了的话，内网最多再等一小会儿
     * （在家时内网一般很快就回了），不用等内网超时。两个地址必须是同一台书库（核对服务器 ID）。
     */
    private fun probeRoutes(): String? {
        val cands = mutableListOf<Pair<String, String>>()
        if (config.lan.isNotEmpty()) cands += "lan" to config.lan
        if (config.remote.isNotEmpty() && config.remote != config.lan) cands += "remote" to config.remote
        if (cands.isEmpty() && config.lastBase.isNotEmpty()) cands += "lan" to config.lastBase
        // 最后才试：经手机转发（出门、连不上书库地址时）
        if (PhoneRelay.phoneNode(this) != null) cands += "relay" to PhoneRelay.start()
        if (cands.isEmpty()) {
            link.adopt("offline", null)
            return null
        }
        // 各线路同时 ping；这里不轮询，阻塞等下一个结果（或者等到某个时限），手表不会被反复叫醒
        val n = cands.size
        val results = arrayOfNulls<JSONObject>(n)
        val done = BooleanArray(n)
        val deadlines = LongArray(n)
        val ecs = ExecutorCompletionService<Int>(pingPool)
        val t0 = SystemClock.elapsedRealtime()
        cands.forEachIndexed { k, (r, u) ->
            // 经手机转发要多等：手机 App 可能刚被叫醒，先要自己选线路，再加上蓝牙来回
            val t = when (r) { "lan" -> LAN_PING_MS; "relay" -> RELAY_PING_MS; else -> REMOTE_PING_MS }
            deadlines[k] = t0 + t + 1000L
            ecs.submit(Callable {
                results[k] = link.ping(u, t)
                k
            })
        }
        fun good(k: Int): Boolean {
            val res = if (done[k]) results[k] else null
            if (res == null) return false
            val id = res.optString("id")
            if (config.serverId.isNotEmpty() && id.isNotEmpty() && id != config.serverId) return false
            if (config.serverId.isEmpty() && id.isNotEmpty()) config.serverId = id
            return true
        }
        val pick = RoutePick(LAN_GRACE_MS)
        while (true) {
            val now = SystemClock.elapsedRealtime()
            val states = IntArray(n) { k ->
                when {
                    !done[k] && now < deadlines[k] -> RoutePick.WAITING
                    good(k) -> RoutePick.OK
                    else -> RoutePick.FAILED // 不通，或者过了时间还没回
                }
            }
            // 经手机转发只是退路：别的线路都确定不通了才用它（在家时内网回得慢一点，也不绕手机走）
            val ri = cands.indexOfFirst { it.first == "relay" }
            if (ri >= 0 && states[ri] == RoutePick.OK && states.indices.any { it != ri && states[it] != RoutePick.FAILED }) states[ri] = RoutePick.WAITING
            when (val k = pick.step(states, now)) {
                RoutePick.NONE -> {
                    link.adopt("offline", null)
                    return null
                }
                RoutePick.WAIT -> {
                    // 最多等到：还在等的那条线路超时，或者等内网的那一小会儿到了
                    var until = pick.graceEndsAt
                    for (j in 0 until n) if (!done[j] && deadlines[j] > now) until = minOf(until, deadlines[j])
                    if (until == Long.MAX_VALUE) until = now + 1000
                    val f = try {
                        ecs.poll(maxOf(1L, until - now), TimeUnit.MILLISECONDS)
                    } catch (e: InterruptedException) {
                        // 调用的线程被叫停（比如听书关了）：不算“不通”
                        Thread.currentThread().interrupt()
                        throw IOException("已取消")
                    }
                    if (f != null) done[f.get()] = true
                }
                else -> {
                    link.adopt(cands[k].first, cands[k].second)
                    return cands[k].second
                }
            }
        }
    }

    // ---------- 阅读进度 ----------

    /** 存进度的结果。conflict：书库上被别的设备更新过（读到了别处），没有覆盖，已经接受了书库的位置 */
    class Saved(val ok: Boolean, val conflict: Boolean = false)

    /** 这本书在书库上的进度版本（updated_at）：打开书时从目录的 resume 里拿，之后每次存成功都更新 */
    fun baseOf(bookId: Long): Long = config.prefs.getLong("base_$bookId", 0)

    private fun setBase(bookId: Long, v: Long) {
        config.prefs.edit().putLong("base_$bookId", v).apply()
    }

    /**
     * 打开书时用了目录里的位置：记下书库进度的版本。旧版书库的目录里没有 updated_at，什么都不做（照旧直接覆盖）。
     * 没网时用的是手表上存的目录，只在还没有记录时才用它（存过进度后记下的更新）。
     */
    fun adoptResume(bookId: Long, toc: JSONObject) {
        val r = toc.optJSONObject("resume") ?: return
        if (!r.has("updated_at")) return
        val v = r.optLong("updated_at")
        val e = config.prefs.edit().putBoolean("progress_base", true)
        if (!toc.optBoolean("_offline") || baseOf(bookId) == 0L) e.putLong("base_$bookId", v)
        e.apply()
    }

    /**
     * 上传阅读进度（在后台线程调用）。带上 base（打开这本书时书库进度的版本）：书库上被别的设备更新过就不覆盖位置，
     * 回 conflict，这里接受书库的位置（不来回覆盖）。连不上时先记下来，下次联网再补传。
     */
    fun saveProgress(bookId: Long, body: JSONObject): Saved {
        synchronized(progressLock) {
            body.put("at", System.currentTimeMillis() / 1000)
            Today.addSeconds(body.optInt("seconds"))
            val base = baseOf(bookId)
            if (base > 0) body.put("base", base) else body.remove("base")
            val r = putProgress(bookId, body)
            if (r == null) {
                val old = config.prefs.getString("pending_$bookId", null)?.let { runCatching { JSONObject(it) }.getOrNull() }
                if (old != null) body.put("seconds", minOf(600, old.optInt("seconds") + body.optInt("seconds")))
                config.prefs.edit().putString("pending_$bookId", body.toString()).apply()
                return Saved(false)
            }
            config.prefs.edit().remove("pending_$bookId").apply()
            return accept(bookId, r)
        }
    }

    /** 传一次。返回书库的回应（JSON），连不上返回 null（留着下次补传） */
    private fun putProgress(bookId: Long, body: JSONObject): JSONObject? = try {
        val r = api("PUT", "/api/books/$bookId/progress", body, timeoutMs = 8000)
        val j = runCatching { JSONObject(r.text) }.getOrNull()
        when {
            j != null && j.optBoolean("conflict") -> j
            r.ok -> j ?: JSONObject()
            r.code in 400..499 -> JSONObject() // 书库不收（书删了等）：补传也没用，不留着
            else -> null
        }
    } catch (e: Unauthorized) {
        JSONObject() // 没法再传了，不要一直留着
    } catch (e: Exception) {
        null
    }

    /** 书库的回应：记下新的进度版本；冲突时接受书库的位置 */
    private fun accept(bookId: Long, j: JSONObject): Saved {
        if (j.optBoolean("conflict")) {
            val v = j.optJSONObject("current")?.optLong("updated_at")?.takeIf { it > 0 } ?: j.optLong("updated_at")
            // 手表上记的位置作废，下次打开用书库的
            config.prefs.edit().putBoolean("progress_base", true).putLong("base_$bookId", v).remove("pos_$bookId").apply()
            return Saved(true, conflict = true)
        }
        if (j.has("updated_at")) setBase(bookId, j.optLong("updated_at"))
        return Saved(true)
    }

    /** 补传离线时记下的进度 */
    fun flushPending() {
        synchronized(progressLock) { flushLocked() }
    }

    private fun flushLocked() {
        val keys = config.prefs.all.keys.filter { it.startsWith("pending_") }
        for (k in keys) {
            val id = k.removePrefix("pending_").toLongOrNull() ?: continue
            val body = config.prefs.getString(k, null)?.let { runCatching { JSONObject(it) }.getOrNull() }
            if (body == null) {
                config.prefs.edit().remove(k).apply()
                continue
            }
            if (!netUsable()) return
            try {
                val r: JSONObject
                if (body.optLong("base") > 0 && config.bool("progress_base", false)) {
                    // 书库会比对 base：之后在手机或网页上读过的话，保留那边的位置，只补上阅读时长
                    r = putProgress(id, body) ?: return
                } else {
                    // 旧版书库：先比一下时间
                    val cur = json("/api/books/$id/progress", 8000)
                    r = if (cur.optLong("updated_at") > body.optLong("at")) {
                        val keep = JSONObject().put("file_id", cur.optLong("file_id")).put("location", cur.optString("location"))
                            .put("percent", cur.optDouble("percent")).put("device", cur.optString("device")).put("seconds", body.optInt("seconds"))
                            .put("at", System.currentTimeMillis() / 1000).put("base", cur.optLong("updated_at"))
                        config.prefs.edit().remove("pos_$id").apply()
                        putProgress(id, keep) ?: return
                    } else putProgress(id, body) ?: return
                }
                config.prefs.edit().remove(k).apply()
                accept(id, r)
            } catch (e: Exception) {
                return // 还是连不上，下次再说
            }
        }
    }

    /** 还没传上去的阅读时长（秒） */
    fun pendingSeconds(): Int = config.prefs.all.entries.filter { it.key.startsWith("pending_") }
        .sumOf { e -> runCatching { JSONObject(e.value.toString()).optInt("seconds") }.getOrDefault(0) }

    companion object {
        lateinit var app: WearApp
            private set
        const val ACTION_CONFIGURED = "app.shuku.wear.CONFIGURED"
        const val ACTION_TTS = "app.shuku.wear.TTS"
        const val ACTION_LISTEN = "app.shuku.wear.LISTEN_STATE"
        const val CH_LISTEN = "listen"
        const val DEVICE = "Wear OS 手表"
        const val OFFLINE = "连不上书库（内网和 Tailscale 都不通）"
        const val NO_NETWORK = "手表没有联网"

        private const val LAN_PING_MS = 3500
        private const val REMOTE_PING_MS = 5000
        private const val RELAY_PING_MS = 12000
        private const val LAN_GRACE_MS = 400L
        private const val STALE_MS = 2 * 60_000L
        private const val OFFLINE_HOLD_MS = 20_000L
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
