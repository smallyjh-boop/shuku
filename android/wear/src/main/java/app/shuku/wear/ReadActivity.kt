package app.shuku.wear

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Bundle
import android.os.SystemClock
import android.view.Gravity
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.view.ViewTreeObserver
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import org.json.JSONArray
import org.json.JSONObject
import kotlin.math.abs
import kotlin.math.max
import kotlin.math.min

/**
 * 在手表上读书：书库把正文按章节切好（GET /api/books/{id}/chapters），这里一章一页地滚动阅读，
 * 用表冠滚动。进度换算成网页阅读器认识的位置（sec:节:比例 / p:页:0）传回书库，手机、网页能接着读。
 * 读过的这一章和下一章存在手表上，路上没网也能接着读。
 * 点“听书”（章节开头）或者点一下正文再点“从这里听”，就在手表上朗读；听的时候这一页跟着读到的地方走。
 * 存进度时带上打开书时书库进度的版本：在别的设备上读到了别处的话，书库不覆盖，这里跳到那边的位置。
 * Tile 上点“继续读”也打开这里（extras：id、title），所以要 exported。
 */
class ReadActivity : Activity() {
    private val app get() = WearApp.app
    private var bookId = 0L
    private var bookTitle = ""
    private var fileId = 0L
    private lateinit var files: BookFiles
    private var chapters = JSONArray()
    private var cur = -1
    private var chapter: JSONObject? = null // 正在显示的一章；加载中、出错时为 null（不保存进度）
    private var sv: ScrollView? = null
    private var textView: TextView? = null
    private var listenButton: TextView? = null
    private var bar: TextView? = null
    private var activeSince = 0L
    private var lastLoc: String? = null
    private var lastPercent = -1.0
    private var loadSeq = 0
    private var doneSeq = 0
    private var retry: () -> Unit = {}
    private val saveTask = Runnable { saveNow() }
    private val hideBar = Runnable { bar?.visibility = View.GONE }

    // 听书时跟着读到的地方走：用户自己在翻（几秒内碰过屏幕、转过表冠）时先不动
    private var resumed = false
    private var hinted = false
    private var lastInput = 0L
    private var followedAt = 0L // 已经跟上的听书位置（时间戳）

    private val sizes = floatArrayOf(13f, 15f, 17f, 19f)
    private var fontSize: Float
        get() = app.config.prefs.getFloat("font", 15f)
        set(v) = app.config.prefs.edit().putFloat("font", v).apply()

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            updateListenButton()
            if (resumed) follow(force = false)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        bookId = intent.getLongExtra("id", 0)
        bookTitle = intent.getStringExtra("title") ?: ""
        if (bookId <= 0 || !app.config.configured) {
            // 从 Tile 打开、但还没连书库（或者没带书）：回首页
            startActivity(Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            finish()
            return
        }
        files = BookFiles(this, bookId)
        val f = IntentFilter(WearApp.ACTION_LISTEN)
        if (Build.VERSION.SDK_INT >= 33) registerReceiver(receiver, f, Context.RECEIVER_NOT_EXPORTED) else registerReceiver(receiver, f)
        openBook()
    }

    override fun onResume() {
        super.onResume()
        resumed = true
        activeSince = SystemClock.elapsedRealtime()
        updateListenButton()
        follow(force = true)
    }

    override fun onPause() {
        resumed = false
        saveNow(force = true)
        activeSince = 0
        super.onPause()
    }

    override fun onDestroy() {
        try { unregisterReceiver(receiver) } catch (e: Exception) {}
        app.main.removeCallbacks(saveTask)
        app.main.removeCallbacks(hideBar)
        super.onDestroy()
    }

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        lastInput = SystemClock.uptimeMillis()
        return super.dispatchTouchEvent(ev)
    }

    override fun dispatchGenericMotionEvent(ev: MotionEvent): Boolean {
        lastInput = SystemClock.uptimeMillis()
        return super.dispatchGenericMotionEvent(ev)
    }

    private fun bg(task: () -> Unit) = app.io.execute(task)
    private fun ui(task: () -> Unit) = runOnUiThread { if (!isDestroyed) task() }

    // ---------- 加载 ----------

    private fun openBook() {
        message("正在打开…")
        retry = { openBook() }
        val my = ++loadSeq
        bg {
            val r = runCatching {
                app.flushPending()
                files.toc()
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
                var i: Int
                var frac: Double
                val listen = ListenService.state()
                if (listen.active && listen.bookId == bookId && listen.fileId == fileId) {
                    // 正在手表上听这本书：从读到的地方开始看（进度由听书来存）
                    i = listen.chapterIndex
                    frac = listen.frac
                } else {
                    // 联网时用书库上的位置，没网时用手表上次记下的位置
                    val p = files.openPoint(toc)
                    i = p.first
                    frac = p.second
                }
                if (frac.isNaN()) frac = 0.0
                followedAt = System.currentTimeMillis()
                Today.noteBook(bookId, bookTitle)
                open(i.coerceIn(0, chapters.length() - 1), frac, quiet = true)
            }
        }
    }

    /** 打开第 i 章的 frac 处。quiet：停在书库（或听书）原来的位置，没翻动就不存回书库 */
    private fun open(i: Int, frac: Double, quiet: Boolean = false) {
        if (chapter != null && i != cur) saveNow(force = true)
        chapter = null // 加载期间不保存进度
        retry = { open(i, frac, quiet) }
        val my = ++loadSeq
        // 手表上存着这一章时几乎是立刻显示，不闪“正在加载”
        app.main.postDelayed({ if (my == loadSeq && doneSeq != my && !isDestroyed) message("正在加载…") }, 250)
        val fid = fileId
        val meta = chapters.optJSONObject(i)
        val nextMeta = chapters.optJSONObject(i + 1)
        bg {
            val r = runCatching { files.chapter(i, fid, meta) }
            ui {
                if (my != loadSeq) return@ui
                doneSeq = my
                r.onSuccess { show(i, it, frac, quiet) }.onFailure { fail(it) }
            }
            // 顺便把下一章存到手表上，点“下一章”马上就能看，没网也能接着读
            if (r.isSuccess && nextMeta != null) runCatching { files.chapter(i + 1, fid, nextMeta) }
            files.prune(i, fid)
        }
    }

    // ---------- 显示 ----------

    private fun message(text: String, canRetry: Boolean = false) {
        chapter = null
        textView = null
        sv = null
        listenButton = null
        bar = null
        val (_, col) = Ui.page(this)
        if (bookTitle.isNotEmpty()) col.addView(Ui.text(this, bookTitle, 14f, Ui.ACCENT, bold = true, center = true, maxLines = 2))
        col.addView(Ui.text(this, text, 13f, if (canRetry) Ui.DANGER else Ui.INK2, center = true), Ui.lp(this, 10f))
        if (canRetry) col.addView(Ui.button(this, "重试", primary = true) {
            app.retryNow() // 不管刚才是不是不通，马上重新连
            retry()
        }, Ui.lp(this, 12f))
    }

    private fun fail(e: Throwable) {
        if (e is WearApp.Unauthorized) message(e.message ?: "请重新连接书库")
        else message(e.message ?: "打开失败", canRetry = true)
    }

    private fun show(i: Int, ch: JSONObject, frac: Double, quiet: Boolean = false) {
        cur = i
        lastLoc = null
        val (s, col) = Ui.page(this, overlay = true)
        val fs = fontSize
        val count = chapters.length()
        if (i > 0) col.addView(Ui.button(this, "上一章") { open(i - 1, 0.0) }, Ui.lp(this, 0f, matchWidth = false))
        col.addView(Ui.text(this, ch.optString("title").ifEmpty { bookTitle }, fs + 1, Ui.ACCENT, bold = true, center = true), Ui.lp(this, 8f))
        listenButton = Ui.button(this, "听书") { onListenButton() }.also { col.addView(it, Ui.lp(this, 6f, matchWidth = false)) }
        val tv = Ui.text(this, BookText.format(ch.optString("text")), fs, serif = true).apply {
            setLineSpacing(0f, 1.5f)
            // 点一下正文：出来“从这里听”
            setOnClickListener { showBar() }
        }
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
        // 浮在屏幕下方的“从这里听”，点一下正文出来，几秒后自己收起
        val root = s.parent as FrameLayout
        bar = Ui.button(this, "从这里听", primary = true) { listenHere() }.apply { visibility = View.GONE }
            .also { root.addView(it, FrameLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL)
                .apply { bottomMargin = (resources.displayMetrics.heightPixels * 0.1f).toInt() }) }
        sv = s
        textView = tv
        chapter = ch
        updateListenButton()
        // 头几次打开书时自己出来一下，让人知道可以从这里听
        if (!hinted && !ListenService.listening(bookId)) {
            hinted = true
            val n = app.config.prefs.getInt("listen_hint", 0)
            if (n < 5) {
                app.config.prefs.edit().putInt("listen_hint", n + 1).apply()
                showBar()
            }
        }
        // 排版完成后滚到上次的位置
        tv.viewTreeObserver.addOnGlobalLayoutListener(object : ViewTreeObserver.OnGlobalLayoutListener {
            override fun onGlobalLayout() {
                tv.layout ?: return
                tv.viewTreeObserver.removeOnGlobalLayoutListener(this)
                if (frac > 0.0) scrollToFrac(frac, smooth = false)
                // 停在原来的位置：当作已经存过，翻动了才存（不白白更新书库上的进度版本，免得正在别处读的人被跳回来）
                if (quiet) here()?.let {
                    lastLoc = it.first
                    lastPercent = it.second
                }
                scheduleSave()
            }
        })
        s.setOnScrollChangeListener { _, _, _, _, _ -> scheduleSave() }
    }

    /** 滚到这一章比例 frac 处（那一行放在屏幕上方） */
    private fun scrollToFrac(frac: Double, smooth: Boolean) {
        val tv = textView ?: return
        val s = sv ?: return
        val l = tv.layout ?: return
        if (tv.text.isEmpty()) return
        val off = (frac * tv.text.length).toInt().coerceIn(0, tv.text.length - 1)
        val y = max(0, tv.top + l.getLineTop(l.getLineForOffset(off)) - Ui.dp(this, 24f))
        if (smooth) s.smoothScrollTo(0, y) else s.scrollTo(0, y)
    }

    private fun cycleFont() {
        val ch = chapter ?: return
        saveNow() // 翻动过还没存的先存上，换字号后停在同一处就不再存
        val frac = position()
        val k = sizes.indexOfFirst { it >= fontSize - 0.1f }
        fontSize = sizes[(k + 1) % sizes.size]
        show(cur, ch, frac, quiet = true)
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

    // ---------- 听书 ----------

    private fun updateListenButton() {
        listenButton?.text = if (ListenService.listening(bookId)) "正在听书" else "听书"
    }

    /** 章节开头的按钮：在听就打开播放页，没在听就从屏幕上这一行开始读 */
    private fun onListenButton() {
        if (ListenService.listening(bookId)) openPlayer() else listenHere()
    }

    private fun showBar() {
        val b = bar ?: return
        b.visibility = View.VISIBLE
        app.main.removeCallbacks(hideBar)
        app.main.postDelayed(hideBar, 4000)
    }

    /** 从屏幕上方这一行开始读 */
    private fun listenHere() {
        if (chapter == null) return
        app.main.removeCallbacks(hideBar)
        bar?.visibility = View.GONE
        saveNow(force = true)
        ListenService.start(this, bookId, bookTitle, cur, position())
        followedAt = System.currentTimeMillis()
        openPlayer()
    }

    private fun openPlayer() {
        startActivity(Intent(this, ListenActivity::class.java).putExtra("from", bookId))
    }

    /** 跟到听书读到的地方（听书读到的这一句比已经跟上的新） */
    private fun follow(force: Boolean) {
        if (chapter == null) return
        val p = ListenService.state()
        if (!p.active || p.bookId != bookId || p.fileId != fileId) return
        val at = p.posAt
        if (at <= followedAt) return
        if (!force && SystemClock.uptimeMillis() - lastInput < 8000) return
        followedAt = at
        val i = p.chapterIndex
        val frac = p.frac
        if (i != cur && i in 0 until chapters.length()) {
            chapter = null // 换章前不要把这一页的旧位置存回书库
            open(i, frac, quiet = true)
        } else if (i == cur) scrollToFrac(frac, smooth = !force)
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

    private fun scheduleSave() {
        app.main.removeCallbacks(saveTask)
        app.main.postDelayed(saveTask, 2500)
    }

    /** 现在读到的地方：（网页阅读器认识的位置，全书百分比，这一章里的比例） */
    private fun here(): Triple<String, Double, Double>? {
        val ch = chapter ?: return null
        var frac = position()
        val last = cur == chapters.length() - 1
        if (last && atEnd()) frac = 1.0
        return Triple(BookPos.location(chapters, cur, frac), BookPos.percent(ch, frac, last && frac >= 1.0), frac)
    }

    private fun saveNow(force: Boolean = false) {
        app.main.removeCallbacks(saveTask)
        // 正在听这本书：进度由听书来存，这一页只是跟着走
        if (ListenService.listening(bookId)) return
        val (loc, percent, frac) = here() ?: return
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
        Today.noteBook(id, bookTitle, percent)
        bg {
            if (app.saveProgress(id, body).conflict) ui { onConflict() }
        }
    }

    /**
     * 书库上的位置被别的设备更新过（在手机、网页上读到了别处），这次没有覆盖：接受书库的位置，跳过去。
     * 之后再翻页就按新的版本存，不会两边来回覆盖。
     */
    private fun onConflict() {
        if (ListenService.listening(bookId)) return // 在听这本书：听书那边会跟过去，这一页跟着听书走
        val my = ++loadSeq
        bg {
            val toc = runCatching { files.toc() }.getOrNull()?.takeIf { !it.optBoolean("_offline") }
            ui {
                if (my != loadSeq || toc == null) return@ui
                if (toc.optLong("file_id") != fileId) {
                    openBook() // 书库那边换了一个文件读：重新打开
                    return@ui
                }
                chapters = toc.optJSONArray("chapters") ?: chapters
                val (i, frac) = files.openPoint(toc)
                if (i == cur && abs(frac - position()) < 0.02) return@ui
                Toast.makeText(this, "在别处读到了新的地方，已跳过去", Toast.LENGTH_SHORT).show()
                chapter = null // 跳之前不要把这一页的旧位置存回书库
                open(i.coerceIn(0, max(0, chapters.length() - 1)), frac, quiet = true)
            }
        }
    }

    companion object {
        private const val REQ_TOC = 1
    }
}
