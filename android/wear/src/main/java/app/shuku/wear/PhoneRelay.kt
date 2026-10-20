package app.shuku.wear

import android.content.Context
import android.os.Looper
import android.os.SystemClock
import android.util.Base64
import app.shuku.core.Relay
import com.google.android.gms.tasks.Tasks
import com.google.android.gms.wearable.Wearable
import org.json.JSONObject
import java.io.BufferedInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/**
 * 手表出门、连不上书库时，请手机代为转发（见 app.shuku.core.Relay）。
 * 在手表本机 127.0.0.1 起一个 HTTP 小服务：书库的其他代码照常请求 http://127.0.0.1:端口/随机串/api/...，
 * 这里把请求经 Wear 数据层交给手机，手机用它已经连好的书库地址去取，结果分块传回。
 * 地址里带一段随机串：手表上别的 App 不知道它，借不了这条路去访问书库。
 * 转发这一层出了问题（手机不在、没回应、手机也连不上书库）不回 HTTP，直接断开：
 * 请求的一方当成连不上（IOException），重新选线路（比如换成手表自己的 Wi-Fi）。
 */
object PhoneRelay {
    private class Reply(val status: Int, val type: String, val body: ByteArray)

    private val pending = ConcurrentHashMap<String, LinkedBlockingQueue<Relay.Chunk>>()
    private val pool = Executors.newCachedThreadPool()

    @Volatile var base: String? = null
        private set
    @Volatile private var nodeId: String? = null
    @Volatile private var nodeAt = -1L
    private var server: ServerSocket? = null
    private val secret = "/r" + UUID.randomUUID().toString().replace("-", "").take(16)

    /** 起本机转发服务（只起一次），返回它的地址 */
    @Synchronized
    fun start(): String {
        base?.let { return it }
        val ss = ServerSocket(0, 16, InetAddress.getByName("127.0.0.1"))
        server = ss
        val b = "http://127.0.0.1:${ss.localPort}$secret"
        base = b
        Thread({
            while (!ss.isClosed) {
                val s = try {
                    ss.accept()
                } catch (e: IOException) {
                    return@Thread
                }
                pool.execute {
                    try {
                        handle(s)
                    } catch (e: Exception) {
                    } finally {
                        runCatching { s.close() }
                    }
                }
            }
        }, "phone-relay").apply { isDaemon = true }.start()
        return b
    }

    /** 手机现在是不是连着（结果缓存 30 秒；没连着也缓存，免得每个请求都等）。不能在主线程调用（会等） */
    fun phoneNode(ctx: Context): String? {
        val now = SystemClock.elapsedRealtime()
        if (nodeAt < 0 || now - nodeAt >= 30_000) refresh(ctx)
        return nodeId
    }

    /** 主线程上只看缓存：有没有手机，不去等 */
    fun phoneLikely(): Boolean = nodeId != null

    /** 后台先查一次手机在不在 */
    fun refreshAsync(ctx: Context) {
        pool.execute { runCatching { refresh(ctx) } }
    }

    private fun refresh(ctx: Context) {
        if (Looper.myLooper() == Looper.getMainLooper()) return // Tasks.await 不能在主线程等
        val nodes = try {
            Tasks.await(Wearable.getNodeClient(ctx).connectedNodes, 3, TimeUnit.SECONDS)
        } catch (e: Exception) {
            emptyList()
        }
        nodeId = nodes.firstOrNull()?.id
        nodeAt = SystemClock.elapsedRealtime()
    }

    /** 手机那边可能已经断开：下次请求前重新找 */
    fun forgetNode() {
        nodeAt = -1L
        nodeId = null
    }

    /** 收到手机的一块返回数据（WatchListener 调用） */
    fun onChunk(bytes: ByteArray) {
        val c = try {
            Relay.decodeChunk(bytes)
        } catch (e: Exception) {
            return
        }
        pending[c.id]?.offer(c)
    }

    // ---------- 本机 HTTP ----------

    private fun handle(s: Socket) {
        s.soTimeout = 60_000
        val inp = BufferedInputStream(s.getInputStream())
        val first = readLine(inp) ?: return
        val parts = first.split(" ")
        if (parts.size < 2 || !parts[1].startsWith("$secret/")) return // 不是书库自己的请求
        val headers = HashMap<String, String>()
        while (true) {
            val h = readLine(inp) ?: break
            if (h.isEmpty()) break
            val c = h.indexOf(':')
            if (c > 0) headers[h.substring(0, c).trim().lowercase()] = h.substring(c + 1).trim()
        }
        val len = headers["content-length"]?.toIntOrNull() ?: 0
        val body = readBody(inp, len)
        val req = JSONObject()
            .put("m", parts[0])
            .put("p", parts[1].removePrefix(secret))
            .put("h", JSONObject().put("Authorization", headers["authorization"] ?: "").put("Content-Type", headers["content-type"] ?: ""))
            .put("b", if (body.isEmpty()) "" else Base64.encodeToString(body, Base64.NO_WRAP))
        val r = forward(WearApp.app, req) ?: return // 转发失败：断开，让请求的一方重新选线路
        val head = "HTTP/1.1 ${r.status} ${reason(r.status)}\r\n" +
            "Content-Type: ${r.type.ifEmpty { "application/octet-stream" }}\r\n" +
            "Content-Length: ${r.body.size}\r\nConnection: close\r\n\r\n"
        val out = s.getOutputStream()
        out.write(head.toByteArray(Charsets.US_ASCII))
        out.write(r.body)
        out.flush()
    }

    private fun forward(ctx: Context, req: JSONObject): Reply? {
        val node = phoneNode(ctx) ?: return null
        val id = UUID.randomUUID().toString()
        val q = LinkedBlockingQueue<Relay.Chunk>()
        pending[id] = q
        try {
            req.put("id", id)
            Tasks.await(Wearable.getMessageClient(ctx).sendMessage(node, Relay.PATH_REQ, req.toString().toByteArray()), 6, TimeUnit.SECONDS)
        } catch (e: Exception) {
            pending.remove(id)
            forgetNode()
            return null
        }
        try {
            var total = -1
            var status = 200
            var type = ""
            val got = HashMap<Int, ByteArray>()
            while (total < 0 || got.size < total) {
                // 每一块最多等 40 秒（合成语音、下载章节可能慢）
                val c = q.poll(40, TimeUnit.SECONDS) ?: return null
                if (c.type == Relay.FAIL) return null // 手机也连不上书库
                total = c.total
                status = c.status
                type = c.type
                got[c.index] = c.data
            }
            val bo = ByteArrayOutputStream()
            for (i in 0 until total) bo.write(got[i] ?: ByteArray(0))
            return Reply(status, type, bo.toByteArray())
        } finally {
            pending.remove(id)
        }
    }

    private fun reason(code: Int) = when (code) {
        200 -> "OK"
        401 -> "Unauthorized"
        403 -> "Forbidden"
        404 -> "Not Found"
        503 -> "Service Unavailable"
        504 -> "Gateway Timeout"
        else -> if (code in 200..299) "OK" else "Error"
    }

    private fun readLine(inp: InputStream): String? {
        val bo = ByteArrayOutputStream()
        while (true) {
            val b = inp.read()
            if (b < 0) return if (bo.size() == 0) null else bo.toString(Charsets.UTF_8.name())
            if (b == '\n'.code) break
            if (b != '\r'.code) bo.write(b)
        }
        return bo.toString(Charsets.UTF_8.name())
    }

    private fun readBody(inp: InputStream, len: Int): ByteArray {
        if (len <= 0) return ByteArray(0)
        val buf = ByteArray(len)
        var off = 0
        while (off < len) {
            val n = inp.read(buf, off, len - off)
            if (n < 0) break
            off += n
        }
        return buf.copyOf(off)
    }
}
