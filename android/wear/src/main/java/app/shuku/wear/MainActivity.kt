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
import android.view.ViewGroup
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
    private var visible = false

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            when (i.action) {
                WearApp.ACTION_CONFIGURED -> {
                    loadedAt = 0
                    render()
                    if (app.config.configured) load()
                    Today.updateViews()
                }
                // 看不见的时候不重画（onResume 时再画）
                WearApp.ACTION_TTS -> if (visible) {
                    renderTts()
                    schedulePhonePoll()
                }
                WearApp.ACTION_LISTEN -> if (visible) renderListen()
            }
        }
    }

    /**
     * 手机听书时不会每读一句就告诉手表（省电）：首页看得见、手机又正在读的时候，每 25 秒问一次读到哪句了；
     * 首页看不见（onPause）就不问。手机开始、暂停、换书时会自己发过来。
     */
    private val phonePoll = Runnable {
        if (visible && phoneReading()) {
            bg { PhoneLink.send(this, "/shuku/tts/state") }
            schedulePhonePoll()
        }
    }

    private fun schedulePhonePoll() {
        app.main.removeCallbacks(phonePoll)
        if (visible && phoneReading()) app.main.postDelayed(phonePoll, 25_000)
    }

    private fun phoneReading(): Boolean {
        val s = runCatching { JSONObject(app.config.str("tts_state", "")) }.getOrNull() ?: return false
        val fresh = System.currentTimeMillis() - app.config.prefs.getLong("tts_at", 0) < 3 * 3600_000L
        return fresh && s.optBoolean("active") && s.optBoolean("playing")
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
        app.main.removeCallbacks(phonePoll)
        super.onDestroy()
    }

    override fun onResume() {
        super.onResume()
        visible = true
        if (shownConfigured != app.config.configured) render()
        renderListen()
        renderTts()
        if (app.config.configured) {
            if (System.currentTimeMillis() - loadedAt > 30_000) load()
            // 请手机发一次听书状态；手机正在读的话，看得见期间隔一会儿再问
            bg { PhoneLink.send(this, "/shuku/tts/state") }
            schedulePhonePoll()
        }
    }

    override fun onPause() {
        visible = false
        app.main.removeCallbacks(phonePoll)
        super.onPause()
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
        col.addView(Ui.text(this, "点下面的按钮，手机会自动把连接信息发过来（手机上要打开书库 App，手表要连着手机）。", 13f, Ui.INK2, center = true), Ui.lp(this, 8f))
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
        // 手机不在身边、或者没装手机 App 时才需要手输地址（平时不用输）
        col.addView(Ui.button(this, "没有手机？手动输入地址") { startActivity(Intent(this, ConnectActivity::class.java)) }, Ui.lp(this, 10f))
    }

    // ---------- 已连接 ----------

    private fun home() {
        col.addView(Ui.text(this, app.config.name.ifEmpty { "书库" }, 17f, bold = true, center = true, maxLines = 1))
        statusView = Ui.text(this, "", 11f, Ui.INK2, center = true).also { col.addView(it, Ui.lp(this, 2f)) }
        // 今天读了多久（和 Tile、表盘上的一样）
        val mins = Today.info().seconds / 60
        if (mins > 0) col.addView(Ui.text(this, "今天读了 $mins 分钟", 12f, Ui.INK2, center = true), Ui.lp(this, 2f))
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
        // 找书、刷新、更多（断开书库）：图标一排，长按显示是什么
        val find = Ui.iconButton(this, android.R.drawable.ic_menu_search, "找书") { startActivity(Intent(this, BooksActivity::class.java)) }
        val refresh = Ui.iconButton(this, android.R.drawable.ic_popup_sync, "刷新") {
            app.retryNow() // 不管刚才是不是不通，马上重新连
            loadedAt = 0
            load()
        }
        val more = Ui.iconButton(this, android.R.drawable.ic_menu_more, "更多") { moreMenu() }
        for (b in listOf(find, refresh, more)) b.setOnLongClickListener { Toast.makeText(this, it.contentDescription, Toast.LENGTH_SHORT).show(); true }
        col.addView(Ui.row(this, find, refresh, more), Ui.lp(this, 14f, matchWidth = false))
    }

    /** 更多：不常用的放这里（断开书库要再确认一次） */
    private fun moreMenu() {
        Pick.choose(this, "更多", listOf("断开书库"), -1) {
            Pick.choose(this, "断开书库？手表上准备的离线听书也会删掉", listOf("断开", "取消"), -1) { k ->
                if (k == 0) disconnect()
            }
        }
    }

    private fun disconnect() {
        ListenService.stopNow()
        // 顺手在书库的设备列表里注销这块手表；手表上准备的离线听书删掉
        bg {
            runCatching { app.api("POST", "/api/auth/logout", JSONObject(), 5000) }
            app.config.clear()
            app.config.prefs.edit().remove("connect_error").remove("reading_cache").apply()
            runCatching { ListenStore.clearAll(app) }
            Today.clear()
            ui { render() }
        }
    }

    private fun routeLabel() = when (app.link.route) {
        "lan" -> "内网"
        "remote" -> "Tailscale"
        "relay" -> "手机转发"
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
                // Tile、表盘复杂功能上的“今天读了多少”顺便也更新一下（刚连上书库，最多两分钟一次）
                Today.refresh(force = true)
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
        val p = Ui.dp(this, 12f)
        if (!s.active) {
            // 没在听：最近读的那本，一点就接着听 / 接着读
            val b = Today.info().book
            if (b == null) {
                box.visibility = View.GONE
                return
            }
            box.visibility = View.VISIBLE
            box.background = Ui.pill(Ui.CARD, Ui.dp(this, 18f).toFloat())
            box.setPadding(p, Ui.dp(this, 10f), p, Ui.dp(this, 10f))
            box.gravity = Gravity.CENTER_HORIZONTAL
            box.isClickable = false
            box.setOnClickListener(null)
            box.addView(Ui.text(this, "上次读到", 11f, Ui.ACCENT, center = true), Ui.lp(this))
            val pct = (b.percent * 100).roundToInt()
            box.addView(Ui.text(this, b.title + if (pct > 0) "　$pct%" else "", 14f, bold = true, center = true, maxLines = 2), Ui.lp(this, 2f))
            val row = LinearLayout(this).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER
            }
            val g = Ui.dp(this, 3f)
            row.addView(Ui.button(this, "继续听", primary = true) {
                startActivity(Intent(this, ListenActivity::class.java).putExtra(ListenActivity.EXTRA_RESUME, b.id).putExtra("title", b.title))
            }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginEnd = g })
            row.addView(Ui.button(this, "继续读") {
                startActivity(Intent(this, ReadActivity::class.java).putExtra("id", b.id).putExtra("title", b.title))
            }, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = g })
            box.addView(row, Ui.lp(this, 8f, matchWidth = false))
            return
        }
        box.visibility = View.VISIBLE
        box.background = Ui.pill(Ui.CARD, Ui.dp(this, 18f).toFloat())
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
        // 手机的音量（戴着耳机听手机时，不用掏手机）：图标加手机现在的音量，点一下拖动或转表冠调
        val pv = s.optInt("volume", -1) // 旧版手机 App 不报音量
        if (pv >= 0) {
            val vol = Ui.chip(this, if (pv == 0) android.R.drawable.ic_lock_silent_mode else android.R.drawable.ic_lock_silent_mode_off, "$pv%") {
                Pick.seek(this, "手机音量", 11, (pv + 5) / 10, { "${it * 10}%" }) { k ->
                    s.put("volume", k * 10)
                    app.config.prefs.edit().putString("tts_state", s.toString()).apply()
                    renderTts()
                    bg { PhoneLink.send(this, "/shuku/tts/volume", (k * 10).toString().toByteArray()) }
                }
            }.apply { contentDescription = "手机音量" }
            box.addView(vol, Ui.lp(this, 6f, matchWidth = false))
        }
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
