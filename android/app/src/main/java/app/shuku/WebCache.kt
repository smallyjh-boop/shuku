package app.shuku

import android.webkit.WebResourceResponse
import app.shuku.core.Http
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.io.File
import java.io.FileInputStream
import java.io.FileOutputStream
import java.io.IOException

/**
 * 书库网页本身（界面、阅读内核）的缓存：联网时从书库取最新版本，断网时用手机上保存的，
 * 所以没网也能打开 App、阅读下载到手机的书。阅读内核（/vendor/）先用缓存，每天检查一次更新。
 */
class WebCache(private val app: ShukuApp) {
    private val dir = File(app.filesDir, "web")
    private val prefs = app.getSharedPreferences("webcache", 0)

    private fun fileOf(path: String): File? {
        var p = path.substringBefore('?').substringBefore('#')
        if (p.isEmpty() || p == "/") p = "/index.html"
        if (p.contains("..")) return null
        return File(dir, p.trimStart('/'))
    }

    fun serve(path: String): WebResourceResponse? {
        val file = fileOf(path) ?: return null
        val meta = File(file.path + ".meta")
        val base = app.link.base
        val vendor = path.startsWith("/vendor/")
        val fresh = vendor && file.exists() && System.currentTimeMillis() - meta.lastModified() < 24 * 3600_000L
        if (base != null && !fresh) {
            try {
                val headers = mutableMapOf<String, String>()
                if (file.exists() && meta.exists()) {
                    val m = JSONObject(meta.readText())
                    m.optString("etag").takeIf { it.isNotEmpty() }?.let { headers["If-None-Match"] = it }
                    m.optString("modified").takeIf { it.isNotEmpty() }?.let { headers["If-Modified-Since"] = it }
                }
                val c = Http.open(base + (if (path == "/" || path.isEmpty()) "/index.html" else path), "GET", null, 4000, headers)
                try {
                    val code = c.responseCode
                    if (code == 304 && file.exists()) {
                        meta.setLastModified(System.currentTimeMillis())
                        return respond(file, meta)
                    }
                    if (code == 200) {
                        file.parentFile?.mkdirs()
                        val tmp = File(file.path + ".part")
                        c.inputStream.use { input -> FileOutputStream(tmp).use { input.copyTo(it) } }
                        tmp.renameTo(file)
                        meta.writeText(JSONObject()
                            .put("type", c.contentType ?: "")
                            .put("etag", c.getHeaderField("ETag") ?: "")
                            .put("modified", c.getHeaderField("Last-Modified") ?: "").toString())
                        return respond(file, meta)
                    }
                } finally {
                    c.disconnect()
                }
            } catch (e: IOException) {
                // 网络不通：用缓存
            }
        }
        if (file.exists()) return respond(file, meta)
        return null
    }

    private fun respond(file: File, meta: File): WebResourceResponse {
        var type = try { JSONObject(meta.readText()).optString("type") } catch (e: Exception) { "" }
        if (type.isEmpty()) type = mimeOf(file.name)
        val mime = type.substringBefore(';').trim()
        val charset = if (mime.startsWith("text/") || mime.endsWith("javascript") || mime.endsWith("json") || mime.endsWith("xml")) "utf-8" else null
        val r = WebResourceResponse(mime, charset, FileInputStream(file))
        r.responseHeaders = mapOf("Cache-Control" to "no-cache", "Access-Control-Allow-Origin" to "*")
        return r
    }

    /**
     * 按书库给的文件清单把整个网页预先缓存下来（只下载有变化的），离线时阅读器的各个部件都在。
     * 在连着书库、App 启动后后台执行。
     */
    fun precache() {
        val base = app.link.base ?: return
        try {
            val r = Http.request("$base/api/assets", timeoutMs = 8000)
            if (!r.ok) return
            val j = JSONObject(r.text)
            val version = j.optString("version")
            if (version.isNotEmpty() && version == prefs.getString("version", "")) return
            val files = j.optJSONArray("files") ?: return
            var failed = false
            for (i in 0 until files.length()) {
                val f = files.getJSONObject(i)
                val p = f.optString("p")
                val local = fileOf(p) ?: continue
                val stamp = "${f.optLong("s")}:${f.optLong("m")}"
                if (local.exists() && prefs.getString("f:$p", "") == stamp) continue
                try {
                    val c = Http.open(base + p, "GET", null, 8000, emptyMap())
                    try {
                        if (c.responseCode != 200) continue
                        local.parentFile?.mkdirs()
                        val tmp = File(local.path + ".part")
                        c.inputStream.use { input -> FileOutputStream(tmp).use { input.copyTo(it) } }
                        tmp.renameTo(local)
                        File(local.path + ".meta").writeText(JSONObject().put("type", c.contentType ?: "")
                            .put("etag", c.getHeaderField("ETag") ?: "").put("modified", c.getHeaderField("Last-Modified") ?: "").toString())
                        prefs.edit().putString("f:$p", stamp).apply()
                    } finally {
                        c.disconnect()
                    }
                } catch (e: IOException) {
                    failed = true
                }
            }
            if (!failed) prefs.edit().putString("version", version).apply()
        } catch (e: Exception) {
            // 下次再试
        }
    }

    companion object {
        fun mimeOf(name: String): String = when (name.substringAfterLast('.', "").lowercase()) {
            "html", "htm" -> "text/html"
            "js", "mjs" -> "text/javascript"
            "css" -> "text/css"
            "json", "webmanifest" -> "application/json"
            "svg" -> "image/svg+xml"
            "png" -> "image/png"
            "jpg", "jpeg" -> "image/jpeg"
            "webp" -> "image/webp"
            "woff2" -> "font/woff2"
            "ttf" -> "font/ttf"
            "pfb" -> "application/x-font-type1"
            "wasm" -> "application/wasm"
            else -> "application/octet-stream"
        }

        fun notFound(): WebResourceResponse = WebResourceResponse("text/plain", "utf-8", 404, "Not Found", mapOf("Access-Control-Allow-Origin" to "*"), ByteArrayInputStream(ByteArray(0)))
    }
}
