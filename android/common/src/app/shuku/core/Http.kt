package app.shuku.core

import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL

/** 一次 HTTP 请求的结果 */
class HttpResult(val code: Int, val body: ByteArray, private val headers: Map<String, List<String>>) {
    val ok: Boolean get() = code in 200..299
    val text: String get() = String(body, Charsets.UTF_8)
    fun header(name: String): String? = headers.entries.firstOrNull { it.key.equals(name, ignoreCase = true) }?.value?.firstOrNull()

    /** 服务器返回的错误说明（{"error": "..."}） */
    fun errorMessage(): String = try {
        org.json.JSONObject(text).optString("error").ifEmpty { "请求失败（$code）" }
    } catch (e: Exception) {
        "请求失败（$code）"
    }
}

/** 用系统自带的 HttpURLConnection，不依赖第三方库 */
object Http {
    @Volatile var userAgent = "ShukuApp"

    /** 响应头（HttpURLConnection 把状态行放在 null 键下，去掉） */
    private fun headersOf(c: HttpURLConnection): Map<String, List<String>> {
        val out = HashMap<String, List<String>>()
        for (e in c.headerFields.entries) {
            val k: String? = e.key
            val v: List<String>? = e.value
            if (k != null && v != null) out[k] = v
        }
        return out
    }

    fun open(url: String, method: String, token: String?, timeoutMs: Int, headers: Map<String, String>): HttpURLConnection {
        val c = URL(url).openConnection() as HttpURLConnection
        c.requestMethod = method
        c.connectTimeout = timeoutMs
        c.readTimeout = maxOf(timeoutMs, 15000)
        c.instanceFollowRedirects = true
        c.useCaches = false
        c.setRequestProperty("User-Agent", userAgent)
        if (!token.isNullOrEmpty()) c.setRequestProperty("Authorization", "Bearer $token")
        for ((k, v) in headers) c.setRequestProperty(k, v)
        return c
    }

    fun request(
        url: String,
        method: String = "GET",
        token: String? = null,
        body: ByteArray? = null,
        contentType: String? = null,
        timeoutMs: Int = 15000,
        headers: Map<String, String> = emptyMap(),
    ): HttpResult {
        val c = open(url, method, token, timeoutMs, headers)
        try {
            if (body != null) {
                c.doOutput = true
                if (contentType != null) c.setRequestProperty("Content-Type", contentType)
                c.setFixedLengthStreamingMode(body.size)
                c.outputStream.use { it.write(body) }
            }
            val code = c.responseCode
            val stream: InputStream? = if (code >= 400) c.errorStream else c.inputStream
            val bytes = stream?.use { it.readBytes() } ?: ByteArray(0)
            return HttpResult(code, bytes, headersOf(c))
        } finally {
            c.disconnect()
        }
    }

    fun getJson(url: String, token: String?, timeoutMs: Int = 15000): org.json.JSONObject {
        val r = request(url, token = token, timeoutMs = timeoutMs)
        if (!r.ok) throw IOException(r.errorMessage())
        return org.json.JSONObject(r.text)
    }

    fun getArray(url: String, token: String?, timeoutMs: Int = 15000): org.json.JSONArray {
        val r = request(url, token = token, timeoutMs = timeoutMs)
        if (!r.ok) throw IOException(r.errorMessage())
        return org.json.JSONArray(r.text)
    }

    fun postJson(url: String, token: String?, json: org.json.JSONObject, method: String = "POST", timeoutMs: Int = 20000): HttpResult =
        request(url, method, token, json.toString().toByteArray(Charsets.UTF_8), "application/json", timeoutMs)

    /** 下载到文件（先写临时文件，完成后改名），返回字节数 */
    fun download(url: String, token: String?, dest: File, onProgress: (done: Long, total: Long) -> Unit = { _, _ -> }): Long {
        val c = open(url, "GET", token, 20000, emptyMap())
        try {
            val code = c.responseCode
            if (code !in 200..299) {
                val msg = c.errorStream?.use { String(it.readBytes(), Charsets.UTF_8) } ?: ""
                throw IOException(HttpResult(code, msg.toByteArray(), emptyMap()).errorMessage())
            }
            val total = c.contentLengthLong
            val tmp = File(dest.path + ".part")
            dest.parentFile?.mkdirs()
            var done = 0L
            c.inputStream.use { input ->
                FileOutputStream(tmp).use { out ->
                    val buf = ByteArray(64 * 1024)
                    var last = 0L
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                        done += n
                        val now = System.currentTimeMillis()
                        if (now - last > 300) {
                            last = now
                            onProgress(done, total)
                        }
                    }
                }
            }
            if (!tmp.renameTo(dest)) {
                tmp.copyTo(dest, overwrite = true)
                tmp.delete()
            }
            onProgress(done, total)
            return done
        } finally {
            c.disconnect()
        }
    }

    /** 把一个流上传到书库的投递目录 */
    fun upload(url: String, token: String?, input: InputStream, length: Long, onProgress: (Long) -> Unit = {}): HttpResult {
        val c = open(url, "POST", token, 20000, emptyMap())
        try {
            c.doOutput = true
            c.setRequestProperty("Content-Type", "application/octet-stream")
            if (length > 0) c.setFixedLengthStreamingMode(length) else c.setChunkedStreamingMode(64 * 1024)
            c.outputStream.use { out ->
                val buf = ByteArray(64 * 1024)
                var done = 0L
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    out.write(buf, 0, n)
                    done += n
                    onProgress(done)
                }
            }
            val code = c.responseCode
            val bytes = (if (code >= 400) c.errorStream else c.inputStream)?.use { it.readBytes() } ?: ByteArray(0)
            return HttpResult(code, bytes, headersOf(c))
        } finally {
            c.disconnect()
        }
    }
}
