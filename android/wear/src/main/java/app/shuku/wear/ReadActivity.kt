package app.shuku.wear

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.os.SystemClock
import android.view.Gravity
import android.view.ViewGroup
import android.view.ViewTreeObserver
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.Locale
import kotlin.math.abs
import kotlin.math.max
import kotlin.math.min

/**
 * 在手表上读书：书库把正文按章节切好（GET /api/books/{id}/chapters），这里一章一页地滚动阅读，
 * 用表冠滚动。进度换算成网页阅读器认识的位置（sec:节:比例 / p:页:0）传回书库，手机、网页能接着读。
 * 读过的这一章和下一章存在手表上，路上没网也能接着读。
 */
class ReadActivity : Activity() {
    private val app get() = WearApp.app
    private var bookId = 0L
    private var bookTitle = ""
    private var fileId = 0L
    private var chapters = JSONArray()
    private var cur = -1
    private var chapter: JSONObject? = null // 正在显示的一章；加载中、出错时为 null（不保存进度）
    private var sv: ScrollView? = null
    private var textView: TextView? = null
    private var activeSince = 0L
    private var lastLoc: String? = null
    private var lastPercent = -1.0
    private var loadSeq = 0
    private var doneSeq = 0
    private var retry: () -> Unit = {}
    private val saveTask = Runnable { saveNow() }

    private val sizes = floatArrayOf(13f, 15f, 17f, 19f)
    private var fontSize: Float
        get() = app.config.prefs.getFloat("font", 15f)
        set(v) = app.config.prefs.edit().putFloat("font", v).apply()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        bookId = intent.getLongExtra("id", 0)
        bookTitle = intent.getStringExtra("title") ?: ""
        openBook()
    }

    override fun onResume() {
        super.onResume()
        activeSince = SystemClock.elapsedRealtime()
    }

    override fun onPause() {
        saveNow(force = true)
        activeSince = 0
        super.onPause()
    }

    override fun onDestroy() {
        app.main.removeCallbacks(saveTask)
        super.onDestroy()
    }

    private fun bg(task: () -> Unit) = app.io.execute(task)
    private fun ui(task: () -> Unit) = runOnUiThread { if (!isDestroyed) task() }

    // ---------- 加载 ----------

    private fun cache(name: String) = File(cacheDir, "books/$bookId/$name")

    /** 联网取，取到了存一份；连不上就用手表上存的 */
    private fun fetch(path: String, name: String): JSONObject {
        val f = cache(name)
        return try {
            val j = app.json(path, 20000)
            f.parentFile?.mkdirs()
            f.writeText(j.toString())
            j
        } catch (e: WearApp.Unauthorized) {
            throw e
        } catch (e: Exception) {
            if (!f.exists()) throw e
            JSONObject(f.readText()).put("_offline", true)
        }
    }

    private fun openBook() {
        message("正在打开…")
        retry = { openBook() }
        val my = ++loadSeq
        bg {
            val r = runCatching {
                app.flushPending()
                fetch("/api/books/$bookId/chapters", "toc.json")
            }
            ui {
                if (my != loadSeq) return@ui
                val toc = r.getOrElse { fail(it); return@ui }
                fileId = toc.optLong("file_id")
                chapters = toc.optJSONArray("chapters") ?: JSONArray()
                if (bookTitle.isEmpty()) bookTitle = toc.optString("title")
                if (chapters.length() == 0) {
                    message("这本书没有可以显示的文字")
                    return@ui
                }
                val resume = toc.optJSONObject("resume")
                var i = resume?.optInt("i") ?: 0
                var frac = resume?.optDouble("frac", 0.0) ?: 0.0
                if (toc.optBoolean("_offline")) {
                    // 没网：用手表上次记下的位置
                    runCatching { JSONObject(app.config.str("pos_$bookId", "")) }.getOrNull()?.let {
                        if (it.optLong("file") == fileId) {
                            i = it.optInt("i")
                            frac = it.optDouble("frac", 0.0)
                        }
                    }
                }
                if (frac.isNaN()) frac = 0.0
                open(i.coerceIn(0, chapters.length() - 1), frac)
            }
        }
    }

    private fun open(i: Int, frac: Double) {
        if (chapter != null && i != cur) saveNow(force = true)
        chapter = null // 加载期间不保存进度
        retry = { open(i, frac) }
        val my = ++loadSeq
        // 手表上存着这一章时几乎是立刻显示，不闪“正在加载”
        app.main.postDelayed({ if (my == loadSeq && doneSeq != my && !isDestroyed) message("正在加载…") }, 250)
        val fid = fileId
        val meta = chapters.optJSONObject(i)
        val nextMeta = chapters.optJSONObject(i + 1)
        bg {
            val r = runCatching { chapterJson(i, fid, meta) }
            ui {
                if (my != loadSeq) return@ui
                doneSeq = my
                r.onSuccess { show(i, it, frac) }.onFailure { fail(it) }
            }
            // 顺便把下一章存到手表上，点“下一章”马上就能看，没网也能接着读
            if (r.isSuccess && nextMeta != null) runCatching { chapterJson(i + 1, fid, nextMeta) }
            prune(i, fid)
        }
    }

    /** 一章的正文：手表上存着（而且和目录对得上）就直接用，否则联网取 */
    private fun chapterJson(i: Int, fid: Long, meta: JSONObject?): JSONObject {
        val f = cache("c${fid}_$i.json")
        if (meta != null && f.exists()) {
            val c = runCatching { JSONObject(f.readText()) }.getOrNull()
            if (c != null && c.optInt("start") == meta.optInt("start") && c.optInt("chars") == meta.optInt("chars")) return c
        }
        return fetch("/api/books/$bookId/chapters/$i?file=$fid", "c${fid}_$i.json")
    }

    /** 每本书只留附近几章，手表上最多存 5 本 */
    private fun prune(i: Int, fid: Long) {
        val re = Regex("""c(\d+)_(\d+)\.json""")
        val dir = File(cacheDir, "books/$bookId")
        dir.listFiles()?.forEach { f ->
            val m = re.matchEntire(f.name) ?: return@forEach
            val n = m.groupValues[2].toInt()
            if (m.groupValues[1].toLong() != fid || n < i - 1 || n > i + 2) f.delete()
        }
        dir.setLastModified(System.currentTimeMillis())
        File(cacheDir, "books").listFiles()?.sortedByDescending { it.lastModified() }?.drop(5)?.forEach { it.deleteRecursively() }
    }

    // ---------- 显示 ----------

    private fun message(text: String, canRetry: Boolean = false) {
        chapter = null
        textView = null
        sv = null
        val (_, col) = Ui.page(this)
        if (bookTitle.isNotEmpty()) col.addView(Ui.text(this, bookTitle, 14f, Ui.ACCENT, bold = true, center = true, maxLines = 2))
        col.addView(Ui.text(this, text, 13f, if (canRetry) Ui.DANGER else Ui.INK2, center = true), Ui.lp(this, 10f))
        if (canRetry) col.addView(Ui.button(this, "重试", primary = true) { retry() }, Ui.lp(this, 12f))
    }

    private fun fail(e: Throwable) {
        if (e is WearApp.Unauthorized) message(e.message ?: "请重新连接书库")
        else message(e.message ?: "打开失败", canRetry = true)
    }

    /** 每段前空两格 */
    private fun format(raw: String): String =
        raw.split('\n').map { it.trim() }.filter { it.isNotEmpty() }.joinToString("\n") { "　　$it" }

    private fun show(i: Int, ch: JSONObject, frac: Double) {
        cur = i
        lastLoc = null
        val (s, col) = Ui.page(this)
        val fs = fontSize
        val count = chapters.length()
        if (i > 0) col.addView(Ui.button(this, "上一章") { open(i - 1, 0.0) }, Ui.lp(this, 0f, matchWidth = false))
        col.addView(Ui.text(this, ch.optString("title").ifEmpty { bookTitle }, fs + 1, Ui.ACCENT, bold = true, center = true), Ui.lp(this, 8f))
        val tv = Ui.text(this, format(ch.optString("text")), fs, serif = true).apply { setLineSpacing(0f, 1.5f) }
        col.addView(tv, Ui.lp(this, 10f))
        col.addView(Ui.text(this, "${i + 1} / $count", 11f, Ui.INK2, center = true), Ui.lp(this, 16f))
        if (i + 1 < count) col.addView(Ui.button(this, "下一章", primary = true) { open(i + 1, 0.0) }, Ui.lp(this, 8f))
        else col.addView(Ui.text(this, "全书完", 14f, Ui.INK2, center = true), Ui.lp(this, 8f))
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
        }
        val gap = Ui.dp(this, 3f)
        row.addView(Ui.button(this, "目录") { toc() }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginEnd = gap })
        row.addView(Ui.button(this, "字号") { cycleFont() }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = gap })
        col.addView(row, Ui.lp(this, 8f, matchWidth = false))
        if (ch.optBoolean("_offline")) col.addView(Ui.text(this, "没联网，这一章是手表上存的", 10f, Ui.INK2, center = true), Ui.lp(this, 8f))
        sv = s
        textView = tv
        chapter = ch
        // 排版完成后滚到上次的位置
        tv.viewTreeObserver.addOnGlobalLayoutListener(object : ViewTreeObserver.OnGlobalLayoutListener {
            override fun onGlobalLayout() {
                val l = tv.layout ?: return
                tv.viewTreeObserver.removeOnGlobalLayoutListener(this)
                if (frac > 0.0 && tv.text.isNotEmpty()) {
                    val off = (frac * tv.text.length).toInt().coerceIn(0, tv.text.length - 1)
                    val y = tv.top + l.getLineTop(l.getLineForOffset(off)) - Ui.dp(this@ReadActivity, 24f)
                    s.scrollTo(0, max(0, y))
                }
                scheduleSave()
            }
        })
        s.setOnScrollChangeListener { _, _, _, _, _ -> scheduleSave() }
    }

    private fun cycleFont() {
        val ch = chapter ?: return
        val frac = position()
        val k = sizes.indexOfFirst { it >= fontSize - 0.1f }
        fontSize = sizes[(k + 1) % sizes.size]
        show(cur, ch, frac)
    }

    private fun toc() {
        if (chapter == null) return
        saveNow(force = true)
        startActivityForResult(Intent(this, TocActivity::class.java).putExtra("id", bookId).putExtra("current", cur), REQ_TOC)
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_TOC && resultCode == RESULT_OK && data != null) {
            val i = data.getIntExtra("i", -1)
            if (i in 0 until chapters.length() && i != cur) open(i, 0.0)
        }
    }

    // ---------- 进度 ----------

    /** 屏幕顶上那一行在这一章里的位置（0～1） */
    private fun position(): Double {
        val tv = textView ?: return 0.0
        val s = sv ?: return 0.0
        val l = tv.layout ?: return 0.0
        val y = s.scrollY - tv.top + Ui.dp(this, 24f)
        if (y <= 0) return 0.0
        val off = l.getLineStart(l.getLineForVertical(y))
        return (off.toDouble() / max(1, tv.text.length)).coerceIn(0.0, 1.0)
    }

    private fun atEnd(): Boolean {
        val s = sv ?: return false
        val child = s.getChildAt(0) ?: return false
        return s.scrollY + s.height >= child.height - Ui.dp(this, 8f)
    }

    /** 换算成网页阅读器认识的位置 */
    private fun location(frac: Double): String {
        val loc = chapters.optJSONObject(cur)?.optString("loc") ?: return ""
        val next = chapters.optJSONObject(cur + 1)?.optString("loc") ?: ""
        if (loc.startsWith("sec:")) {
            // 一节太长时拆成了几章：loc 里的比例是这一章在整节里的起点，下一章的起点就是终点
            val p = loc.split(":")
            val sec = p.getOrNull(1) ?: return loc
            val f0 = p.getOrNull(2)?.toDoubleOrNull() ?: 0.0
            val np = next.split(":")
            val f1 = if (next.startsWith("sec:") && np.getOrNull(1) == sec) np.getOrNull(2)?.toDoubleOrNull() ?: 1.0 else 1.0
            return "sec:$sec:" + String.format(Locale.US, "%.4f", (f0 + frac * (f1 - f0)).coerceIn(0.0, 1.0))
        }
        if (loc.startsWith("p:")) {
            // PDF 几页合成一章：按比例估算在第几页
            val first = loc.split(":").getOrNull(1)?.toIntOrNull() ?: return loc
            val nextFirst = if (next.startsWith("p:")) next.split(":").getOrNull(1)?.toIntOrNull() else null
            val page = if (nextFirst != null && nextFirst > first) first + min((frac * (nextFirst - first)).toInt(), nextFirst - first - 1) else first
            return "p:$page:0"
        }
        return "" // MOBI、FB2 等整本一段文字的格式，只能按百分比
    }

    private fun scheduleSave() {
        app.main.removeCallbacks(saveTask)
        app.main.postDelayed(saveTask, 2500)
    }

    private fun saveNow(force: Boolean = false) {
        app.main.removeCallbacks(saveTask)
        val ch = chapter ?: return
        var frac = position()
        val last = cur == chapters.length() - 1
        if (last && atEnd()) frac = 1.0
        val start = ch.optInt("start")
        val chars = ch.optInt("chars")
        val total = max(1, ch.optInt("total"))
        val percent = if (last && frac >= 1.0) 1.0 else ((start + frac * chars) / total).coerceIn(0.0, 1.0)
        val loc = location(frac)
        val now = SystemClock.elapsedRealtime()
        val secs = if (activeSince > 0) ((now - activeSince) / 1000).toInt() else 0
        val same = loc == lastLoc && abs(percent - lastPercent) < 0.001
        if (same && (!force || secs < 5)) return
        if (activeSince > 0) activeSince = now
        lastLoc = loc
        lastPercent = percent
        app.config.prefs.edit().putString("pos_$bookId", JSONObject().put("file", fileId).put("i", cur).put("frac", frac).toString()).apply()
        val body = JSONObject().put("file_id", fileId).put("location", loc).put("percent", percent)
            .put("device", WearApp.DEVICE).put("seconds", min(600, secs))
        val id = bookId
        bg { app.saveProgress(id, body) }
    }

    companion object {
        private const val REQ_TOC = 1
    }
}
