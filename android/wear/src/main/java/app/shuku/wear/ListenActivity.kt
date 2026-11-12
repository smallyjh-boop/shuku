package app.shuku.wear

import android.Manifest
import android.app.Activity
import android.media.AudioManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import java.util.Locale
import kotlin.math.ceil

/**
 * 手表上的听书播放页：正在读的这一句、上一句 / 暂停 / 下一句、语速、定时、回到文字，
 * 已经准备好多少离线听的语音、“多准备一些（离线听）”。
 * 没连耳机时提示去连（Wear OS 建议听东西用蓝牙耳机），手表有扬声器的话也可以选用扬声器。
 * Tile、表盘复杂功能上点“继续听”也打开这里（带 EXTRA_RESUME），所以要 exported。
 */
class ListenActivity : Activity() {
    private val app get() = WearApp.app
    private lateinit var titleView: TextView
    private lateinit var chapterView: TextView
    private lateinit var textView: TextView
    private lateinit var statusView: TextView
    private lateinit var outputBox: LinearLayout
    private lateinit var speakerButton: TextView
    private lateinit var controls: LinearLayout
    private lateinit var hintView: TextView
    private lateinit var playButton: Ui.Action
    private lateinit var options: LinearLayout
    private lateinit var speedButton: Ui.Action
    private lateinit var timerButton: Ui.Action
    private lateinit var volumeButton: Ui.Action
    private lateinit var offlineView: TextView
    private lateinit var voiceButton: Ui.Action
    private lateinit var voiceRow: View
    private lateinit var iconRow: LinearLayout
    private lateinit var moreButton: Ui.Action
    private lateinit var backButton: TextView

    private var visible = false

    // 听书的状态变了：看得见的时候才刷新（看不见时 onResume 再刷新）
    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            if (visible) update()
        }
    }

    // 音量变了（转表冠、按耳机上的键、系统音量条）：更新音量图标旁的数字
    private val volumeReceiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            if (visible) updateVolume()
        }
    }

    // 定了时：看得见的时候每半分钟刷新一次“还剩几分”；没定时不刷新
    private val tick = Runnable { update() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        build()
        val f = IntentFilter(WearApp.ACTION_LISTEN)
        if (Build.VERSION.SDK_INT >= 33) registerReceiver(receiver, f, Context.RECEIVER_NOT_EXPORTED) else registerReceiver(receiver, f)
        // 系统发的音量变化广播
        val vf = IntentFilter("android.media.VOLUME_CHANGED_ACTION")
        if (Build.VERSION.SDK_INT >= 33) registerReceiver(volumeReceiver, vf, Context.RECEIVER_EXPORTED) else registerReceiver(volumeReceiver, vf)
        // 通知栏里的播放控制（只问一次）
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED &&
            !app.config.bool("asked_notifications", false)) {
            app.config.prefs.edit().putBoolean("asked_notifications", true).apply()
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        // 系统重建页面时（savedInstanceState 不为空）不要再开始一遍
        if (savedInstanceState == null) handleLaunch(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleLaunch(intent)
        update()
    }

    /**
     * Tile、表盘复杂功能上的“继续听”：从这本书上次的位置接着听（EXTRA_RESUME 是书的 ID，-1 表示 Tile 上现在显示的那本）。
     * 已经在听这本书就接着放。在这个页面里启动听书服务：页面在前台，Android 12 起才允许启动前台服务。
     */
    private fun handleLaunch(i: Intent?) {
        val rid = i?.getLongExtra(EXTRA_RESUME, 0L) ?: 0L
        if (rid == 0L) return
        i?.removeExtra(EXTRA_RESUME)
        if (!app.config.configured) {
            ListenService.lastReason = "还没连接书库。请在手表上打开书库，连接以后再听。"
            return
        }
        var id = rid
        var title = i?.getStringExtra("title") ?: ""
        if (id < 0) {
            val b = Today.info().book
            if (b == null) {
                ListenService.lastReason = "还没有在读的书。打开一本书，点“听书”。"
                return
            }
            id = b.id
            title = b.title
        }
        if (ListenService.listening(id)) ListenService.play()
        else try {
            ListenService.start(this, id, title, -1, 0.0)
        } catch (e: IllegalStateException) {
            // 系统不让启动前台服务（理论上页面在前台时不会）：提示再点一次
            ListenService.lastReason = "没能开始听书，请点一下“返回”再试一次。"
        }
    }

    override fun onResume() {
        super.onResume()
        visible = true
        update()
    }

    override fun onPause() {
        visible = false
        app.main.removeCallbacks(tick)
        super.onPause()
    }

    override fun onDestroy() {
        try { unregisterReceiver(receiver) } catch (e: Exception) {}
        try { unregisterReceiver(volumeReceiver) } catch (e: Exception) {}
        app.main.removeCallbacks(tick)
        super.onDestroy()
    }

    private fun build() {
        val (_, col) = Ui.page(this)
        titleView = Ui.text(this, "", 14f, Ui.ACCENT, bold = true, center = true, maxLines = 2).also { col.addView(it, Ui.lp(this)) }
        chapterView = Ui.text(this, "", 11f, Ui.INK2, center = true, maxLines = 1).also { col.addView(it, Ui.lp(this, 2f)) }
        textView = Ui.text(this, "", 15f, serif = true, center = true, maxLines = 6).apply { setLineSpacing(0f, 1.3f) }
            .also { col.addView(it, Ui.lp(this, 10f)) }
        statusView = Ui.text(this, "", 11f, Ui.INK2, center = true).also { col.addView(it, Ui.lp(this, 6f)) }
        // 长按书名、章节名、正文：拖到别的章（比一章一章点“上一章”“下一章”快）
        for (v in listOf(titleView, chapterView, textView)) v.setOnLongClickListener { seekChapter(); true }
        hintView = Ui.text(this, "长按文字可以拖到别的章", 10f, Ui.INK2, center = true).also { col.addView(it, Ui.lp(this, 2f)) }

        // 没连耳机
        outputBox = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            visibility = View.GONE
        }
        outputBox.addView(Ui.text(this, "听书要先连上蓝牙耳机", 13f, center = true), Ui.lp(this))
        outputBox.addView(Ui.button(this, "连接耳机", primary = true) { AudioOut.choose(this) }, Ui.lp(this, 8f))
        speakerButton = Ui.button(this, "用手表扬声器") { ListenService.useSpeaker() }.also { outputBox.addView(it, Ui.lp(this, 6f)) }
        col.addView(outputBox, Ui.lp(this, 8f))

        // 上一句、播放 / 暂停、下一句
        playButton = Ui.action(this, R.drawable.ic_a_pause, "", primary = true) { ListenService.toggle() }
        controls = Ui.actions(this,
            Ui.action(this, R.drawable.ic_a_prev, "") { ListenService.prev() }.apply { desc("上一句") },
            playButton,
            Ui.action(this, R.drawable.ic_a_next, "") { ListenService.next() }.apply { desc("下一句") })
        col.addView(controls, Ui.lp(this, 10f))

        // 语速、定时、音量：点语速、定时弹出来选；音量下面是现在的音量，点一下出来系统的音量条（转表冠、按耳机上的键也行）
        speedButton = Ui.action(this, null, "语速") { pickSpeed() }
        timerButton = Ui.action(this, R.drawable.ic_a_timer, "定时") { pickTimer() }
        volumeButton = Ui.action(this, R.drawable.ic_a_volume, "音量") { showVolume() }
        options = Ui.actions(this, speedButton, timerButton, volumeButton)
        col.addView(options, Ui.lp(this, 10f))

        // 离线听：已经准备好多少
        offlineView = Ui.text(this, "", 11f, Ui.INK2, center = true).also { col.addView(it, Ui.lp(this, 8f)) }

        // 看文字、离线听（多准备一些，开着时亮）、停止
        moreButton = Ui.action(this, R.drawable.ic_a_download, "离线听") {
            val on = !ListenService.state().more
            ListenService.setMore(on)
            Toast.makeText(this, if (on) "多准备约半小时，离开手机也能听" else "不用多准备了", Toast.LENGTH_SHORT).show()
        }
        iconRow = Ui.actions(this,
            Ui.action(this, R.drawable.ic_a_book, "看文字") { openText() },
            moreButton,
            Ui.action(this, R.drawable.ic_a_stop, "停止") {
                ListenService.stopNow()
                finish()
            })
        col.addView(iconRow, Ui.lp(this, 10f))
        // 读法：书库在线合成，还是手表自带语音（不联网也能读）
        voiceButton = Ui.action(this, R.drawable.ic_a_headset, "读法") { pickVoice(this) { update() } }
        voiceRow = Ui.actions(this, voiceButton)
        col.addView(voiceRow, Ui.lp(this, 10f))
        backButton = Ui.button(this, "返回") { finish() }.apply { visibility = View.GONE }
        col.addView(backButton, Ui.lp(this, 10f, matchWidth = false))
    }

    private fun update() {
        val s = ListenService.state()
        app.main.removeCallbacks(tick)
        if (visible && s.active && s.timer > 0) app.main.postDelayed(tick, 30_000)
        if (!s.active) {
            // 停了（读完了、在别处点了停止）
            titleView.text = "听书"
            chapterView.text = ""
            textView.text = s.status.ifEmpty { "没有在听的书。打开一本书，点“听书”。" }
            statusView.text = ""
            outputBox.visibility = View.GONE
            controls.visibility = View.GONE
            hintView.visibility = View.GONE
            options.visibility = View.GONE
            offlineView.visibility = View.GONE
            iconRow.visibility = View.GONE
            voiceRow.visibility = View.GONE
            backButton.visibility = View.VISIBLE
            return
        }
        controls.visibility = View.VISIBLE
        updateVolume()
        // 提示长按跳章：头几次看到时显示，书只有一章时不用
        hintView.visibility = if (s.chapterCount > 1 && app.config.prefs.getInt("seek_hint", 0) < 5) View.VISIBLE else View.GONE
        options.visibility = View.VISIBLE
        iconRow.visibility = View.VISIBLE
        voiceRow.visibility = View.VISIBLE
        voiceButton.caption(if (ListenService.voiceSource() == "device") "手表语音" else "书库语音")
        voiceButton.on(ListenService.voiceSource() == "device")
        backButton.visibility = View.GONE
        titleView.text = s.title
        chapterView.text = if (s.chapterCount > 1) "${s.chapterIndex + 1}/${s.chapterCount}　${s.chapter}" else s.chapter
        textView.text = s.text.ifEmpty { if (s.status.isEmpty()) "准备中…" else "" }
        statusView.text = when {
            s.needOutput -> ""
            s.status.isNotEmpty() -> s.status
            !s.playing -> "已暂停"
            s.deviceVoice -> "用手表自带语音朗读"
            else -> ""
        }
        statusView.visibility = if (statusView.text.isEmpty()) View.GONE else View.VISIBLE
        outputBox.visibility = if (s.needOutput) View.VISIBLE else View.GONE
        speakerButton.visibility = if (s.needOutput && AudioOut.hasSpeaker(this)) View.VISIBLE else View.GONE
        playButton.icon(if (s.playing) R.drawable.ic_a_pause else R.drawable.ic_a_play)
        playButton.desc(if (s.playing) "暂停" else "继续")
        speedButton.text(String.format(Locale.US, "%.1f×", s.speed))
        // 定了时：图标亮着，下面写还剩多久
        timerButton.on(s.timer != 0)
        timerButton.caption(when {
            s.timer == -1 -> "读完本章"
            s.timer > 0 -> "还剩 ${ceil(s.timerLeftMs / 60_000.0).toInt()} 分"
            else -> "定时"
        })
        offlineView.text = offlineLine(s)
        offlineView.visibility = if (offlineView.text.isEmpty()) View.GONE else View.VISIBLE
        moreButton.on(s.more)
    }

    /** 系统的音量条（Wear OS 上可以直接转表冠调） */
    private fun showVolume() {
        val am = getSystemService(AUDIO_SERVICE) as AudioManager
        am.adjustStreamVolume(AudioManager.STREAM_MUSIC, AudioManager.ADJUST_SAME, AudioManager.FLAG_SHOW_UI)
        updateVolume()
    }

    private fun updateVolume() {
        val am = getSystemService(AUDIO_SERVICE) as AudioManager
        val max = am.getStreamMaxVolume(AudioManager.STREAM_MUSIC)
        val cur = am.getStreamVolume(AudioManager.STREAM_MUSIC)
        volumeButton.caption(if (max > 0) "音量 ${cur * 100 / max}%" else "音量")
        // 静音时换成静音图标
        volumeButton.icon(if (cur == 0) R.drawable.ic_a_volume_off else R.drawable.ic_a_volume)
    }

    /** 点语速：列出来直接选 */
    private fun pickSpeed() {
        val s = ListenService.state()
        val speeds = ListenService.SPEEDS
        val cur = speeds.indexOfFirst { kotlin.math.abs(it - s.speed) < 0.01 }
        Pick.choose(this, "语速", speeds.map { String.format(Locale.US, "%.1f×", it) }, cur) { ListenService.setSpeed(speeds[it]) }
    }

    /** 点定时：列出来直接选 */
    private fun pickTimer() {
        val s = ListenService.state()
        val opts = ListenService.TIMERS
        val names = opts.map { if (it == 0) "不定时" else if (it == -1) "读完本章停" else "$it 分钟后停" }
        Pick.choose(this, "睡眠定时", names, opts.indexOf(s.timer)) { ListenService.setTimer(opts[it]) }
    }

    /** 长按：拖到别的章接着听 */
    private fun seekChapter() {
        val s = ListenService.state()
        if (!s.active || s.chapterCount <= 1) return
        app.config.prefs.edit().putInt("seek_hint", 99).apply() // 会用了，不再提示
        Pick.seek(this, "拖到第几章", s.chapterCount, s.chapterIndex, { i ->
            val t = ListenService.chapterTitle(i)
            "${i + 1} / ${s.chapterCount}" + if (t.isNotEmpty()) "\n$t" else ""
        }) { ListenService.jump(this, it) }
    }

    /**
     * 播放时间和储存时间（和手机、网页一样的说法，按现在的语速）：本章已听 / 共多长；
     * 手表上已存多少（平时往后存 1 小时，点了“离线听”存 3 小时），没网时还能听多久
     */
    private fun offlineLine(s: ListenService.State): String {
        val time = if (s.chapTotal > 0) "本章 ${clock(s.chapElapsed)} / ${clock(s.chapTotal)}" else ""
        val min = s.offlineSec / 60
        val mb = if (s.offlineBytes > 0) "（${Math.max(1, s.offlineBytes / (1 shl 20))}MB）" else ""
        val store = when {
            !s.online -> if (min > 0) "没联网，手表已存的还能听约 $min 分钟" else "没联网"
            s.preparing && min < 30 -> if (min > 0) "正在存到手表，已存 $min 分钟$mb" else "正在存到手表…"
            min > 0 -> "手表已存 $min 分钟$mb"
            else -> ""
        }
        return listOf(time, store).filter { it.isNotEmpty() }.joinToString("\n")
    }

    private fun clock(sec: Int): String {
        val h = sec / 3600
        val m = sec / 60 % 60
        val x = sec % 60
        return if (h > 0) String.format(Locale.US, "%d:%02d:%02d", h, m, x) else String.format(Locale.US, "%d:%02d", m, x)
    }

    // 往左滑进阅读（看文字，跟着读到的地方）；往右滑是系统的返回
    private var downX = 0f
    private var downY = 0f
    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        when (ev.actionMasked) {
            MotionEvent.ACTION_DOWN -> { downX = ev.x; downY = ev.y }
            MotionEvent.ACTION_UP -> {
                val dx = ev.x - downX
                val dy = ev.y - downY
                if (dx < -Ui.dp(this, 50f) && kotlin.math.abs(dx) > kotlin.math.abs(dy) * 1.5f) {
                    openText()
                    return true
                }
            }
        }
        return super.dispatchTouchEvent(ev)
    }

    /** 回到文字：从阅读页来的就回去（阅读页会跟到正在读的地方），否则打开这本书 */
    private fun openText() {
        val s = ListenService.state()
        if (!s.active || intent.getLongExtra("from", 0) == s.bookId) {
            finish()
            return
        }
        startActivity(Intent(this, ReadActivity::class.java).putExtra("id", s.bookId).putExtra("title", s.title))
        finish()
    }

    companion object {
        /** 选读法（听书页、阅读页共用）：书库在线合成 / 手表自带语音 */
        fun pickVoice(a: android.app.Activity, done: () -> Unit = {}) {
            val opts = listOf("server", "device")
            val names = listOf("书库语音（在线合成，更自然）", "手表语音（不联网也能读）")
            Pick.choose(a, "读法", names, opts.indexOf(ListenService.voiceSource())) {
                ListenService.setVoiceSource(opts[it])
                Toast.makeText(a, if (opts[it] == "device") "改用手表自带语音" else "改用书库在线合成", Toast.LENGTH_SHORT).show()
                done()
            }
        }

        /** “继续听”：书的 ID，-1 表示 Tile 上现在显示的那本 */
        const val EXTRA_RESUME = "resume"
    }
}
