package app.shuku.wear

import android.content.Context
import org.json.JSONObject
import java.io.File
import kotlin.math.max

/**
 * 一本书在手表上的缓存：目录和附近几章的正文。阅读页和听书共用，路上没网也能接着读、接着听。
 * 书库把正文按章节切好：GET /api/books/{id}/chapters（目录）、/api/books/{id}/chapters/{i}（一章）。
 */
class BookFiles(private val ctx: Context, val bookId: Long) {
    private val app get() = WearApp.app

    fun file(name: String) = File(ctx.cacheDir, "books/$bookId/$name")

    /** 联网取，取到了存一份；连不上就用手表上存的 */
    fun fetch(path: String, name: String): JSONObject {
        val f = file(name)
        return try {
            val j = app.json(path, 20000)
            f.parentFile?.mkdirs()
            // 先写临时文件再改名：阅读页和听书可能同时在存同一个文件
            val tmp = File(f.path + "." + Thread.currentThread().id + ".tmp")
            tmp.writeText(j.toString())
            if (!tmp.renameTo(f)) tmp.delete()
            j
        } catch (e: WearApp.Unauthorized) {
            throw e
        } catch (e: Exception) {
            if (!f.exists()) throw e
            JSONObject(f.readText()).put("_offline", true)
        }
    }

    fun toc(): JSONObject = fetch("/api/books/$bookId/chapters", "toc.json")

    /**
     * 打开这本书时从哪里开始（第几章、这一章里的比例）：联网时用书库的位置（目录里的 resume），
     * 没网时用手表上次记下的位置。同时记下书库进度的版本（resume.updated_at），之后存进度时带上，
     * 书库上被别的设备更新过就不会被这里覆盖。
     */
    fun openPoint(toc: JSONObject): Pair<Int, Double> {
        val count = toc.optJSONArray("chapters")?.length() ?: 0
        val resume = toc.optJSONObject("resume")
        var i = resume?.optInt("i") ?: 0
        var frac = resume?.optDouble("frac", 0.0) ?: 0.0
        if (toc.optBoolean("_offline")) {
            runCatching { JSONObject(app.config.str("pos_$bookId", "")) }.getOrNull()?.let {
                if (it.optLong("file") == toc.optLong("file_id")) {
                    i = it.optInt("i")
                    frac = it.optDouble("frac", 0.0)
                }
            }
        }
        app.adoptResume(bookId, toc)
        if (frac.isNaN()) frac = 0.0
        return i.coerceIn(0, max(0, count - 1)) to frac.coerceIn(0.0, 1.0)
    }

    /** 阅读页刚取过的目录（几分钟内）直接用，不再联网 */
    fun recentToc(maxAgeMs: Long): JSONObject {
        val f = file("toc.json")
        if (f.exists() && System.currentTimeMillis() - f.lastModified() < maxAgeMs) {
            runCatching { JSONObject(f.readText()) }.getOrNull()?.let { return it }
        }
        return toc()
    }

    /** 一章的正文：手表上存着（而且和目录对得上）就直接用，否则联网取 */
    fun chapter(i: Int, fid: Long, meta: JSONObject?): JSONObject {
        val f = file("c${fid}_$i.json")
        if (meta != null && f.exists()) {
            val c = runCatching { JSONObject(f.readText()) }.getOrNull()
            if (c != null && c.optInt("start") == meta.optInt("start") && c.optInt("chars") == meta.optInt("chars")) return c
        }
        return fetch("/api/books/$bookId/chapters/$i?file=$fid", "c${fid}_$i.json")
    }

    /**
     * 每本书只留附近几章，手表上最多存 5 本。正在听的书另外留着听书要用的那几章（后面几章预先取好的正文，
     * 离开手机没网时接着听），也不算在 5 本里。
     */
    fun prune(i: Int, fid: Long) {
        val re = Regex("""c(\d+)_(\d+)\.json""")
        val dir = File(ctx.cacheDir, "books/$bookId")
        val listen = ListenService.keep
        val keep = listen?.takeIf { it.bookId == bookId }
        dir.listFiles()?.forEach { f ->
            val m = re.matchEntire(f.name) ?: return@forEach
            val n = m.groupValues[2].toInt()
            val file = m.groupValues[1].toLong()
            val near = file == fid && n >= i - 1 && n <= i + 2
            val listening = keep != null && file == keep.fileId && n >= keep.from && n <= keep.to
            if (!near && !listening) f.delete()
        }
        dir.setLastModified(System.currentTimeMillis())
        val listenDir = listen?.bookId?.toString()
        File(ctx.cacheDir, "books").listFiles()?.filter { it.name != listenDir }?.sortedByDescending { it.lastModified() }?.drop(5)
            ?.forEach { it.deleteRecursively() }
    }
}
