package app.shuku

import app.shuku.core.Http
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors

/** 把书下载到手机：书的文件（TXT 等用服务器转好的 EPUB）、漫画的每一页、封面、进度、书签 */
class Downloads(private val app: ShukuApp) {
    private val active = ConcurrentHashMap<Int, JSONObject>()
    private val pool = Executors.newSingleThreadExecutor()
    private val store get() = app.store

    fun list(): JSONArray {
        val out = JSONArray()
        for (id in store.ids()) {
            if (active.containsKey(id)) continue
            val b = store.book(id) ?: continue
            out.put(JSONObject().put("id", id).put("title", b.optString("title")).put("author", b.optString("author"))
                .put("state", "done").put("size", store.size(id)).put("format", store.readInfo(id)?.optString("format") ?: ""))
        }
        for (j in active.values) out.put(JSONObject(j.toString()))
        return out
    }

    fun start(id: Int, hint: JSONObject?) {
        if (active[id]?.optString("state") == "downloading") return
        val title = hint?.optString("title") ?: store.book(id)?.optString("title") ?: ""
        active[id] = JSONObject().put("id", id).put("title", title).put("state", "downloading").put("progress", 0)
        emit(id)
        pool.execute { run(id) }
    }

    fun remove(id: Int) {
        active.remove(id)
        store.remove(id)
        emit(id)
    }

    private fun set(id: Int, state: String, progress: Double, error: String? = null) {
        val j = active[id] ?: return
        j.put("state", state).put("progress", progress)
        if (error != null) j.put("error", error)
        emit(id)
    }

    private fun emit(id: Int) {
        val j = active[id] ?: JSONObject().put("id", id).put("state", if (store.isDone(id)) "done" else "removed")
        Events.emit("download", j)
    }

    private fun run(id: Int) {
        val link = app.link
        val token = app.config.token
        val dir = store.dir(id)
        try {
            if (!link.online && link.probe() == null) throw IOException("连不上书库")
            dir.mkdirs()
            File(dir, "state.json").writeText(JSONObject().put("state", "downloading").toString())
            val book = link.get("/api/books/$id")
            book.remove("files")
            val info = link.get("/api/books/$id/read")
            val orig = info.getString("url")
            info.put("orig_url", orig)
            active[id]?.put("title", book.optString("title"))
            if (info.optString("reader") == "comic") {
                // 漫画按页下载（CBR、7z 也一样），离线时由 App 按页提供
                val fid = info.getInt("file_id")
                val pages = link.get("/api/files/$fid/pages")
                val n = pages.getInt("count")
                val pdir = File(dir, "pages").apply { mkdirs() }
                for (i in 0 until n) {
                    val r = Http.request(link.url("/api/files/$fid/pages/$i"), token = token, timeoutMs = 30000)
                    if (!r.ok) throw IOException(r.errorMessage())
                    val ext = when (r.header("Content-Type")?.substringBefore(';')) {
                        "image/png" -> "png"
                        "image/webp" -> "webp"
                        "image/gif" -> "gif"
                        "image/avif" -> "avif"
                        else -> "jpg"
                    }
                    File(pdir, "%04d.%s".format(i, ext)).writeBytes(r.body)
                    set(id, "downloading", (i + 1).toDouble() / n)
                }
                File(dir, "pages.json").writeText(pages.toString())
            } else {
                val ext = info.optString("name").substringAfterLast('.', "").ifEmpty { info.optString("format") }.lowercase()
                dir.listFiles()?.filter { it.name.startsWith("book.") }?.forEach { it.delete() }
                Http.download(link.url(orig), token, File(dir, "book.$ext")) { done, total ->
                    set(id, "downloading", if (total > 0) done.toDouble() / total else 0.0)
                }
            }
            try {
                val r = Http.request(link.url("/api/books/$id/cover"), token = token)
                if (r.ok) File(dir, "cover.jpg").writeBytes(r.body)
            } catch (e: IOException) {}
            try {
                val p = link.get("/api/books/$id/progress")
                if (p.optLong("updated_at") > 0) File(dir, "progress.json").writeText(p.toString())
            } catch (e: Exception) {}
            try { File(dir, "bookmarks.json").writeText(link.getArray("/api/books/$id/bookmarks").toString()) } catch (e: Exception) {}
            File(dir, "book.json").writeText(book.toString())
            File(dir, "read.json").writeText(info.toString())
            File(dir, "state.json").writeText(JSONObject().put("state", "done").put("at", System.currentTimeMillis()).toString())
            active.remove(id)
            emit(id)
        } catch (e: Exception) {
            File(dir, "state.json").writeText(JSONObject().put("state", "error").toString())
            set(id, "error", 0.0, e.message ?: e.javaClass.simpleName)
        }
    }

    /** 连着内网时，把“在读”的书自动下载到手机（漫画太大，不自动下载） */
    fun autoDownload() {
        if (!app.config.bool("auto_download", false) || app.link.route != "lan") return
        try {
            val items = app.link.get("/api/books?reading=1&limit=10&sort=recent").getJSONArray("items")
            for (i in 0 until items.length()) {
                val b = items.getJSONObject(i)
                val id = b.getInt("id")
                if (b.optString("kind") != "comic" && !store.isDone(id) && !active.containsKey(id)) start(id, b)
            }
        } catch (e: Exception) {}
    }
}
