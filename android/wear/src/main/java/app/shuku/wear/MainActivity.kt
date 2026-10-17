package app.shuku.wear

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import org.json.JSONArray
import org.json.JSONObject
import kotlin.math.roundToInt

/**
 * 手表首页：
 *   没连接 → 请手机发连接信息（或手动输入地址和配对码）
 *   已连接 → 手表上正在听的书、手机上正在听的书（可以暂停、上一句、下一句）+ 在读的书，点开就能在手表上读、听
 */
class MainActivity : Activity() {
    private val app get() = WearApp.app
    private lateinit var col: LinearLayout
    private var listenBox: LinearLayout? = null
    private var ttsBox: LinearLayout? = null
    private var listBox: LinearLayout? = null
    private var statusView: TextView? = null
    private var updateView: TextView? = null
    private var shownConfigured: Boolean? = null
    private var loadedAt = 0L
    private var loading = false
    private var waitingPhone: Runnable? = null

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            when (i.action) {
                WearApp.ACTION_CONFIGURED -> {
                    loadedAt = 0
                    render()
                    if (app.config.configured) load()
                }
                WearApp.ACTION_TTS -> renderTts()
                WearApp.ACTION_LISTEN -> renderListen()
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val f = IntentFilter().apply {
            addAction(WearApp.ACTION_CONFIGURED)
            addAction(WearApp.ACTION_TTS)
            addAction(WearApp.ACTION_LISTEN)
        }
        if (Build.VERSION.SDK_INT >= 33) registerReceiver(receiver, f, Context.RECEIVER_NOT_EXPORTED) else registerReceiver(receiver, f)
        render()
    }

    override fun onDestroy() {
        try { unregisterReceiver(receiver) } catch (e: Exception) {}
        waitingPhone?.let { app.main.removeCallbacks(it) }
        super.onDestroy()
    }

    override fun onResume() {
        super.onResume()
        if (shownConfigured != app.config.configured) render()
        renderListen()
        if (app.config.configured) {
            if (System.currentTimeMillis() - loadedAt > 30_000) load()
            // 请手机发一次听书状态
            bg { PhoneLink.send(this, "/shuku/tts/state") }
        }
    }

    private fun bg(task: () -> Unit) = app.io.execute(task)
    private fun ui(task: () -> Unit) = runOnUiThread { if (!isDestroyed) task() }

    private fun render() {
        val (_, c) = Ui.page(this)
        col = c
        listenBox = null
        ttsBox = null
        listBox = null
        statusView = null
        shownConfigured = app.config.configured
        if (app.config.configured) home() else setup()
    }

    // ---------- 还没连接 ----------

    private fun setup() {
        col.addView(Ui.text(this, "书库", 20f, bold = true, center = true))
        col.addView(Ui.text(this, "在手机的书库 App 里打开“设置”，点“连接手表”。也可以在这里请手机发过来。", 13f, Ui.INK2, center = true), Ui.lp(this, 8f))
        val err = app.config.str("connect_error", "")
        val status = Ui.text(this, err, 12f, Ui.DANGER, center = true)
        col.addView(Ui.button(this, "从手机获取", primary = true) {
            status.setTextColor(Ui.INK2)
            status.text = "正在请手机发送…"
            bg {
                val ok = PhoneLink.send(this, "/shuku/config/request")
                ui {
                    if (!ok) {
                        status.setTextColor(Ui.DANGER)
                        status.text = "没找到手机。请确认手表连着手机，手机上装好了书库 App"
                        return@ui
                    }
                    status.text = "已发送请求，等手机回应…"
                    waitingPhone?.let { app.main.removeCallbacks(it) }
                    val r = Runnable {
                        if (!isDestroyed && !app.config.configured && status.text.startsWith("已发送")) {
                            status.setTextColor(Ui.DANGER)
                            status.text = "手机没有回应。请在手机上打开书库 App → 设置 → 连接手表"
                        }
                    }
                    waitingPhone = r
                    app.main.postDelayed(r, 25_000)
                }
            }
        }, Ui.lp(this, 12f))
        col.addView(status, Ui.lp(this, 8f))
        col.addView(Ui.button(this, "手动输入地址") { startActivity(Intent(this, ConnectActivity::class.java)) }, Ui.lp(this, 10f))
    }

    // ---------- 已连接 ----------

    private fun home() {
        col.addView(Ui.text(this, app.config.name.ifEmpty { "书库" }, 17f, bold = true, center = true, maxLines = 1))
        statusView = Ui.text(this, "", 11f, Ui.INK2, center = true).also { col.addView(it, Ui.lp(this, 2f)) }
        updateView = Ui.text(this, "", 12f, Ui.ACCENT, center = true).apply { visibility = View.GONE }.also { col.addView(it, Ui.lp(this, 6f)) }
        listenBox = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            visibility = View.GONE
        }.also { col.addView(it, Ui.lp(this, 8f)) }
        renderListen()
        ttsBox = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            visibility = View.GONE
        }.also { col.addView(it, Ui.lp(this, 8f)) }
        renderTts()
        col.addView(Ui.text(this, "在读", 13f, Ui.ACCENT, bold = true), Ui.lp(this, 12f))
        listBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }.also { col.addView(it, Ui.lp(this, 2f)) }
        // 先显示上次的列表，联网后再换成最新的
        val cached = app.config.str("reading_cache", "")
        if (cached.isNotEmpty()) showBooks(JSONArray(cached)) else listBox!!.addView(Ui.text(this, "正在加载…", 13f, Ui.INK2))
        col.addView(Ui.button(this, "找书") { startActivity(Intent(this, BooksActivity::class.java)) }, Ui.lp(this, 14f))
        col.addView(Ui.button(this, "刷新") { loadedAt = 0; load() }, Ui.lp(this, 8f))
        val dis = Ui.button(this, "断开书库") {}
        dis.setOnClickListener {
            if (dis.tag == null) {
                dis.tag = 1
                dis.text = "再点一次断开"
                dis.setTextColor(Ui.DANGER)
                return@setOnClickListener
            }
            // 顺手在书库的设备列表里注销这块手表
            bg {
                runCatching { app.api("POST", "/api/auth/logout", JSONObject(), 5000) }
                app.config.clear()
                app.config.prefs.edit().remove("connect_error").remove("reading_cache").apply()
                ui { render() }
            }
        }
        col.addView(dis, Ui.lp(this, 8f))
    }

    private fun routeLabel() = when (app.link.route) {
        "lan" -> "内网"
        "remote" -> "Tailscale"
        else -> "离线"
    }

    private fun load() {
        if (loading || !app.config.configured) return
        loading = true
        statusView?.text = "正在连接…"
        bg {
            val res = runCatching {
                app.flushPending()
                app.json("/api/books?reading=1&limit=20")
            }
            // 书库上有新的手表 App 时提示（手表不能自己装，要在手机上点“更新手表”）
            val wear = if (res.isSuccess) runCatching { app.json("/api/clients", 8000).optJSONObject("wear") }.getOrNull() else null
            ui {
                showUpdate(wear)
                loading = false
                val e = res.exceptionOrNull()
                if (e is WearApp.Unauthorized) {
                    render()
                    return@ui
                }
                if (e != null) {
                    statusView?.text = e.message ?: WearApp.OFFLINE
                    return@ui
                }
                loadedAt = System.currentTimeMillis()
                statusView?.text = routeLabel()
                val items = res.getOrThrow().optJSONArray("items") ?: JSONArray()
                app.config.prefs.edit().putString("reading_cache", items.toString()).apply()
                showBooks(items)
            }
        }
    }

    private fun showUpdate(w: JSONObject?) {
        val v = updateView ?: return
        if (w != null && w.optInt("versionCode") > BuildConfig.VERSION_CODE) {
            v.text = "手表 App 有新版本 ${w.optString("versionName")}。在手机书库 App 的“设置 → 版本与升级”里点“更新手表”"
            v.visibility = View.VISIBLE
        } else v.visibility = View.GONE
    }

    private fun showBooks(items: JSONArray) {
        val box = listBox ?: return
        box.removeAllViews()
        var n = 0
        for (i in 0 until items.length()) {
            val b = items.optJSONObject(i) ?: continue
            if (b.optString("kind") == "comic") continue // 漫画在手表上看不了
            n++
            box.addView(bookRow(this, b), Ui.lp(this, 6f))
            if (n >= 12) break
        }
        if (n == 0) box.addView(Ui.text(this, "还没有在读的书。点“找书”挑一本。", 12f, Ui.INK2))
    }

    // ---------- 手表上的听书 ----------

    private fun renderListen() {
        val box = listenBox ?: return
        box.removeAllViews()
        val s = ListenService.state()
        if (!s.active) {
            box.visibility = View.GONE
            return
        }
        box.visibility = View.VISIBLE
        box.background = Ui.pill(Ui.CARD, Ui.dp(this, 18f).toFloat())
        val p = Ui.dp(this, 12f)
        box.setPadding(p, Ui.dp(this, 10f), p, Ui.dp(this, 8f))
        box.gravity = Gravity.CENTER_HORIZONTAL
        box.isClickable = true
        box.setOnClickListener { startActivity(Intent(this, ListenActivity::class.java)) }
        val head = when {
            s.needOutput -> "请连接蓝牙耳机"
            s.playing -> "手表正在朗读"
            else -> "朗读已暂停"
        }
        box.addView(Ui.text(this, head, 11f, Ui.ACCENT, center = true), Ui.lp(this))
        box.addView(Ui.text(this, s.title, 14f, bold = true, center = true, maxLines = 1), Ui.lp(this, 2f))
        val line = s.status.ifEmpty { s.text }
        if (line.isNotEmpty()) box.addView(Ui.text(this, line, 12f, Ui.INK2, serif = true, center = true, maxLines = 2), Ui.lp(this, 2f))
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
        }
        row.addView(Ui.iconButton(this, android.R.drawable.ic_media_previous, "上一句") { ListenService.prev() })
        row.addView(Ui.iconButton(this, if (s.playing) android.R.drawable.ic_media_pause else android.R.drawable.ic_media_play, if (s.playing) "暂停" else "继续") { ListenService.toggle() })
        row.addView(Ui.iconButton(this, android.R.drawable.ic_media_next, "下一句") { ListenService.next() })
        box.addView(row, Ui.lp(this, 6f, matchWidth = false))
    }

    // ---------- 手机上的听书 ----------

    private fun renderTts() {
        val box = ttsBox ?: return
        box.removeAllViews()
        val s = runCatching { JSONObject(app.config.str("tts_state", "")) }.getOrNull()
        val fresh = System.currentTimeMillis() - app.config.prefs.getLong("tts_at", 0) < 3 * 3600_000L
        if (s == null || !s.optBoolean("active") || !fresh) {
            box.visibility = View.GONE
            return
        }
        val playing = s.optBoolean("playing")
        box.visibility = View.VISIBLE
        box.background = Ui.pill(Ui.CARD, Ui.dp(this, 18f).toFloat())
        val p = Ui.dp(this, 12f)
        box.setPadding(p, Ui.dp(this, 10f), p, Ui.dp(this, 8f))
        box.gravity = Gravity.CENTER_HORIZONTAL
        box.addView(Ui.text(this, if (playing) "手机正在朗读" else "朗读已暂停", 11f, Ui.ACCENT, center = true), Ui.lp(this))
        box.addView(Ui.text(this, s.optString("title"), 14f, bold = true, center = true, maxLines = 1), Ui.lp(this, 2f))
        val line = s.optString("status").ifEmpty { s.optString("text") }
        if (line.isNotEmpty()) box.addView(Ui.text(this, line, 12f, Ui.INK2, serif = true, center = true, maxLines = 2), Ui.lp(this, 2f))
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER
        }
        row.addView(Ui.iconButton(this, android.R.drawable.ic_media_previous, "上一句") { tts("prev") })
        row.addView(Ui.iconButton(this, if (playing) android.R.drawable.ic_media_pause else android.R.drawable.ic_media_play, if (playing) "暂停" else "继续") {
            // 先在手表上改过来，手机回状态后再校正
            s.put("playing", !playing)
            app.config.prefs.edit().putString("tts_state", s.toString()).apply()
            renderTts()
            tts(if (playing) "pause" else "resume")
        })
        row.addView(Ui.iconButton(this, android.R.drawable.ic_media_next, "下一句") { tts("next") })
        box.addView(row, Ui.lp(this, 6f, matchWidth = false))
        box.addView(Ui.text(this, "停止", 12f, Ui.INK2, center = true).apply {
            val v = Ui.dp(this@MainActivity, 6f)
            setPadding(v * 3, v, v * 3, v)
            setOnClickListener {
                s.put("active", false)
                app.config.prefs.edit().putString("tts_state", s.toString()).apply()
                renderTts()
                tts("stop")
            }
        }, Ui.lp(this, 2f, matchWidth = false))
    }

    private fun tts(cmd: String) = bg {
        if (!PhoneLink.send(this, "/shuku/tts/$cmd")) ui { Toast.makeText(this, "没连上手机", Toast.LENGTH_SHORT).show() }
    }

    companion object {
        /** 一本书一行：书名 + 作者、进度。点开在手表上读 */
        fun bookRow(a: Activity, b: JSONObject): View {
            val row = LinearLayout(a).apply {
                orientation = LinearLayout.VERTICAL
                background = Ui.pill(Ui.CARD, Ui.dp(a, 14f).toFloat())
                val p = Ui.dp(a, 12f)
                setPadding(p, Ui.dp(a, 8f), p, Ui.dp(a, 8f))
                isClickable = true
            }
            row.addView(Ui.text(a, b.optString("title"), 14f, bold = true, maxLines = 2))
            val pct = (b.optDouble("percent", 0.0) * 100).roundToInt()
            val sub = listOf(b.optString("author"), if (pct > 0) "$pct%" else "").filter { it.isNotEmpty() }.joinToString("　")
            if (sub.isNotEmpty()) row.addView(Ui.text(a, sub, 11f, Ui.INK2, maxLines = 1))
            row.setOnClickListener {
                if (b.optString("kind") == "comic") {
                    Toast.makeText(a, "漫画请在手机或网页上看", Toast.LENGTH_SHORT).show()
                    return@setOnClickListener
                }
                a.startActivity(Intent(a, ReadActivity::class.java).putExtra("id", b.optLong("id")).putExtra("title", b.optString("title")))
            }
            return row
        }
    }
}
