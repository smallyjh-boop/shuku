package app.shuku

import android.net.Uri
import android.webkit.WebResourceResponse
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.io.File
import java.io.FileInputStream

/**
 * 下载到手机的书。每本书一个文件夹：files/books/<书 ID>/
 *   book.json      书的信息（和 /api/books/<ID> 一样）
 *   read.json      打开方式（和 /api/books/<ID>/read 一样）
 *   book.<格式>     书的文件；漫画是 pages/ 下的每一页
 *   cover.jpg、progress.json、bookmarks.json、state.json
 * 断网时 App 用这些文件冒充书库的接口，网页端几乎不用改就能离线阅读。
 */
class OfflineStore(private val app: ShukuApp) {
    val root = File(app.filesDir, "books").apply { mkdirs() }

    fun dir(id: Int) = File(root, id.toString())

    fun isDone(id: Int) = File(dir(id), "state.json").let { it.exists() && readJson(it)?.optString("state") == "done" }

    fun ids(): List<Int> = (root.listFiles() ?: emptyArray()).mapNotNull { it.name.toIntOrNull() }.filter { isDone(it) }

    fun book(id: Int): JSONObject? = readJson(File(dir(id), "book.json"))

    fun readInfo(id: Int): JSONObject? = readJson(File(dir(id), "read.json"))

    /** 文件 ID → 书 ID（只看已下载完的） */
    fun bookOfFile(fid: Int): Int? = ids().firstOrNull { readInfo(it)?.optInt("file_id") == fid }

    fun bookFile(id: Int): File? = dir(id).listFiles()?.firstOrNull { it.name.startsWith("book.") && !it.name.endsWith(".part") }

    fun remove(id: Int) { dir(id).deleteRecursively() }

    fun size(id: Int): Long = dir(id).walkTopDown().filter { it.isFile }.sumOf { it.length() }

    // ---------- 进度：离线时读到哪里也记在这里 ----------

    fun progress(id: Int): JSONObject = readJson(File(dir(id), "progress.json")) ?: JSONObject()

    fun updateProgress(id: Int, body: JSONObject) {
        if (!dir(id).exists()) return
        val p = JSONObject()
            .put("file_id", body.optInt("file_id"))
            .put("location", body.optString("location"))
            .put("percent", body.optDouble("percent", 0.0))
            .put("device", body.optString("device"))
            .put("updated_at", System.currentTimeMillis() / 1000)
        File(dir(id), "progress.json").writeText(p.toString())
        book(id)?.let {
            it.put("percent", p.optDouble("percent")).put("last_read", p.optLong("updated_at"))
            if (p.optDouble("percent") >= 0.995) it.put("read_state", "done") else if (it.optString("read_state") == "unread") it.put("read_state", "reading")
            File(dir(id), "book.json").writeText(it.toString())
        }
    }

    // ---------- 联网时：已经下载的书直接用手机上的文件，打开更快、不费流量 ----------

    /**
     * version：网页请求的地址里 v= 的值（TXT 转换规则的版本）。联网时传进来，和下载时的不一样就不用手机上的旧文件，
     * 去书库取新的；断网时传 null，手机上有什么用什么
     */
    fun serveLocal(path: String, version: String? = null): WebResourceResponse? {
        Regex("^/api/files/(\\d+)/(raw|epub|pdf)$").find(path)?.let { m ->
            val id = bookOfFile(m.groupValues[1].toInt()) ?: return null
            val info = readInfo(id) ?: return null
            val orig = info.optString("orig_url")
            if (orig.substringBefore('?') != path) return null
            if (version != null && version != (Regex("[?&]v=([^&]*)").find(orig)?.groupValues?.get(1) ?: "")) return null
            val f = bookFile(id) ?: return null
            return fileResponse(f, f.extension)
        }
        Regex("^/api/files/(\\d+)/pages(?:/(\\d+))?$").find(path)?.let { m ->
            val id = bookOfFile(m.groupValues[1].toInt()) ?: return null
            return comicPage(id, m.groupValues[2])
        }
        Regex("^/api/books/(\\d+)/cover$").find(path)?.let { m ->
            val f = File(dir(m.groupValues[1].toInt()), "cover.jpg")
            if (isDone(m.groupValues[1].toInt()) && f.exists()) return fileResponse(f, "jpg")
        }
        return null
    }

    private fun comicPage(id: Int, n: String): WebResourceResponse? {
        if (n.isEmpty()) {
            val f = File(dir(id), "pages.json")
            return if (f.exists()) json(f.readText()) else null
        }
        val page = File(dir(id), "pages").listFiles()?.firstOrNull { it.nameWithoutExtension == n.padStart(4, '0') } ?: return null
        return fileResponse(page, page.extension)
    }

    // ---------- 断网时：冒充书库的接口 ----------

    fun emulate(path: String, uri: Uri): WebResourceResponse? {
        if (path == "/api/ping") return null // 让网页真的去探测，才知道网络恢复了
        serveLocal(path)?.let { return it }
        val name = app.config.name.ifEmpty { "我的书库" }
        when {
            path == "/api/auth/state" -> return json(JSONObject().put("setup", true).put("authed", true).put("name", name))
            path == "/api/settings" -> return json(JSONObject().put("server_name", name).put("offline", true)
                .put("network", JSONObject().put("lan_url", app.config.lan).put("remote_url", app.config.remote))
                .put("categories", JSONArray()).put("ai", JSONObject()).put("tts", JSONObject().put("engine", "browser").put("openai", JSONObject()))
                .put("online_meta", JSONObject()).put("fulltext", JSONObject()).put("tools", JSONObject()).put("version", "离线"))
            path == "/api/status" -> return json(JSONObject().put("inbox", JSONObject().put("pending", 0)).put("scan", JSONObject().put("running", false))
                .put("last_event", 0).put("enrich", JSONObject()).put("fulltext", JSONObject()))
            path == "/api/facets" -> return json(facets())
            path == "/api/books" -> return json(list(uri))
            path == "/api/events" || path == "/api/suggestions" || path == "/api/ops" -> return json("[]")
            path == "/api/tts/voices" -> return json(JSONObject().put("engine", "native").put("voices", JSONArray()).put("default", ""))
        }
        Regex("^/api/books/(\\d+)(/(read|progress|bookmarks))?$").find(path)?.let { m ->
            val id = m.groupValues[1].toInt()
            if (!isDone(id)) return error(404, "这本书没有下载到手机，联网后才能打开")
            return when (m.groupValues[3]) {
                "" -> json(book(id)!!.toString())
                "read" -> json(readInfo(id)!!.put("url", "/offline-files/$id").toString())
                "progress" -> json(progress(id).toString())
                else -> json(File(dir(id), "bookmarks.json").takeIf { it.exists() }?.readText() ?: "[]")
            }
        }
        Regex("^/offline-files/(\\d+)$").find(path)?.let { m ->
            val id = m.groupValues[1].toInt()
            val f = bookFile(id) ?: return error(404, "文件不在手机上")
            return fileResponse(f, f.extension)
        }
        return error(503, "现在连不上书库，这个功能要联网才能用")
    }

    private fun facets(): JSONObject {
        val books = ids().mapNotNull { book(it) }
        val kinds = books.groupingBy { it.optString("kind", "book") }.eachCount()
        return JSONObject().put("total", books.size).put("review", 0).put("suggestions", 0)
            .put("reading", books.count { it.optLong("last_read") > 0 && it.optString("read_state") != "done" })
            .put("kinds", JSONArray(kinds.map { JSONObject().put("name", it.key).put("count", it.value) }))
            .put("categories", JSONArray()).put("formats", JSONArray()).put("states", JSONArray()).put("libraries", JSONArray())
    }

    private fun list(uri: Uri): String {
        val q = (uri.getQueryParameter("q") ?: "").trim().lowercase()
        val kind = uri.getQueryParameter("kind") ?: ""
        val reading = uri.getQueryParameter("reading") == "1"
        val state = uri.getQueryParameter("state") ?: ""
        val sort = uri.getQueryParameter("sort") ?: "recent"
        val limit = uri.getQueryParameter("limit")?.toIntOrNull() ?: 120
        val offset = uri.getQueryParameter("offset")?.toIntOrNull() ?: 0
        var books = ids().mapNotNull { book(it) }.filter { b ->
            (q.isEmpty() || q.split(' ').all { t -> listOf("title", "author", "series", "orig_title").any { b.optString(it).lowercase().contains(t) } }) &&
                (kind.isEmpty() || b.optString("kind") == kind) &&
                (state.isEmpty() || b.optString("read_state") == state) &&
                (!reading || (b.optLong("last_read") > 0 && b.optString("read_state") != "done"))
        }
        books = when (sort) {
            "title" -> books.sortedBy { it.optString("title") }
            "author" -> books.sortedBy { it.optString("author") }
            "added" -> books.sortedByDescending { it.optLong("created_at") }
            else -> books.sortedWith(compareByDescending<JSONObject> { it.optLong("last_read") }.thenByDescending { it.optLong("created_at") })
        }
        val items = JSONArray()
        for (b in books.drop(offset).take(limit)) {
            items.put(JSONObject()
                .put("id", b.optInt("id")).put("title", b.optString("title")).put("author", b.optString("author"))
                .put("series", b.optString("series")).put("series_index", b.optDouble("series_index", 0.0))
                .put("category", b.optString("category")).put("kind", b.optString("kind", "book")).put("status", "ok")
                .put("cover", if (File(dir(b.optInt("id")), "cover.jpg").exists()) b.optString("cover").ifEmpty { "local" } else "")
                .put("read_state", b.optString("read_state", "unread")).put("percent", b.optDouble("percent", 0.0))
                .put("last_read", b.optLong("last_read")).put("created_at", b.optLong("created_at"))
                .put("formats", readInfo(b.optInt("id"))?.optString("format") ?: "").put("confidence", 1))
        }
        return JSONObject().put("total", books.size).put("items", items).toString()
    }

    // ---------- 工具 ----------

    fun fileResponse(f: File, format: String): WebResourceResponse {
        val mime = when (format.lowercase()) {
            "epub" -> "application/epub+zip"
            "pdf" -> "application/pdf"
            "mobi", "azw", "azw3" -> "application/x-mobipocket-ebook"
            "fb2" -> "application/x-fictionbook+xml"
            "txt" -> "text/plain"
            "jpg", "jpeg" -> "image/jpeg"
            "png" -> "image/png"
            "webp" -> "image/webp"
            "gif" -> "image/gif"
            else -> "application/octet-stream"
        }
        return WebResourceResponse(mime, null, 200, "OK",
            mapOf("Content-Length" to f.length().toString(), "Cache-Control" to "no-cache", "Access-Control-Allow-Origin" to "*"), FileInputStream(f))
    }

    private fun json(s: String) = WebResourceResponse("application/json", "utf-8", 200, "OK",
        mapOf("Cache-Control" to "no-store", "Access-Control-Allow-Origin" to "*"), ByteArrayInputStream(s.toByteArray(Charsets.UTF_8)))

    private fun json(o: JSONObject) = json(o.toString())

    private fun error(code: Int, msg: String) = WebResourceResponse("application/json", "utf-8", code, if (code == 404) "Not Found" else "Service Unavailable",
        mapOf("Cache-Control" to "no-store", "Access-Control-Allow-Origin" to "*"), ByteArrayInputStream(JSONObject().put("error", msg).toString().toByteArray(Charsets.UTF_8)))

    companion object {
        fun readJson(f: File): JSONObject? = try { if (f.exists()) JSONObject(f.readText()) else null } catch (e: Exception) { null }
    }
}
