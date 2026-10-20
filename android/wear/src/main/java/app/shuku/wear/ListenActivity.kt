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
import android.view.View
import android.view.ViewGroup
import android.widget.ImageButton
import android.widget.LinearLayout
import android.widget.TextView
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
    private lateinit var volumeRow: LinearLayout
    private lateinit var volumeView: TextView
    private lateinit var playButton: ImageButton
    private lateinit var options: LinearLayout
    private lateinit var speedButton: TextView
    private lateinit var timerButton: TextView
    private lateinit var offlineView: TextView
    private lateinit var moreButton: TextView
    private lateinit var textButton: TextView
    private lateinit var stopButton: TextView

    private var visible = false

    // 听书的状态变了：看得见的时候才刷新（看不见时 onResume 再刷新）
    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) {
            if (visible) update()
        }
    }

    // 定了时：看得见的时候每半分钟刷新一次“还剩几分”；没定时不刷新
    private val tick = Runnable { update() }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        build()
        val f = IntentFilter(WearApp.ACTION_LISTEN)
        if (Build.VERSION.SDK_INT >= 33) registerReceiver(receiver, f, Context.RECEIVER_NOT_EXPORTED) else registerReceiver(receiver, f)
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
        app.main.removeCallbacks(tick)
        super.onDestroy()
    }

    private fun row() = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER
    }

    private fun build() {
        val (_, col) = Ui.page(this)
        titleView = Ui.text(this, "", 14f, Ui.ACCENT, bold = true, center = true, maxLines = 2).also { col.addView(it, Ui.lp(this)) }
        chapterView = Ui.text(this, "", 11f, Ui.INK2, center = true, maxLines = 1).also { col.addView(it, Ui.lp(this, 2f)) }
        textView = Ui.text(this, "", 15f, serif = true, center = true, maxLines = 6).apply { setLineSpacing(0f, 1.3f) }
            .also { col.addView(it, Ui.lp(this, 10f)) }
        statusView = Ui.text(this, "", 11f, Ui.INK2, center = true).also { col.addView(it, Ui.lp(this, 6f)) }

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

        controls = row()
        controls.addView(Ui.iconButton(this, android.R.drawable.ic_media_previous, "上一句") { ListenService.prev() })
        playButton = Ui.iconButton(this, android.R.drawable.ic_media_pause, "暂停") { ListenService.toggle() }.also { controls.addView(it) }
        controls.addView(Ui.iconButton(this, android.R.drawable.ic_media_next, "下一句") { ListenService.next() })
        col.addView(controls, Ui.lp(this, 10f, matchWidth = false))

        // 音量：手表没有音量键时用这两个按钮（调的是听书用的媒体音量）
        volumeRow = row()
        volumeRow.addView(Ui.button(this, "音量 −") { adjustVolume(-1) })
        volumeView = Ui.text(this, "", 12f, Ui.INK2, center = true).apply { setPadding(Ui.dp(this@ListenActivity, 8f), 0, Ui.dp(this@ListenActivity, 8f), 0) }
        volumeRow.addView(volumeView)
        volumeRow.addView(Ui.button(this, "音量 +") { adjustVolume(1) })
        col.addView(volumeRow, Ui.lp(this, 6f, matchWidth = false))

        options = row()
        val gap = Ui.dp(this, 3f)
        speedButton = Ui.button(this, "1.0×") { ListenService.cycleSpeed() }
        timerButton = Ui.button(this, "定时") { ListenService.cycleTimer() }
        options.addView(speedButton, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginEnd = gap })
        options.addView(timerButton, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = gap })
        col.addView(options, Ui.lp(this, 8f, matchWidth = false))

        // 离线听：已经准备好多少；多准备一些（约 1 小时）
        offlineView = Ui.text(this, "", 11f, Ui.INK2, center = true).also { col.addView(it, Ui.lp(this, 10f)) }
        moreButton = Ui.button(this, "多准备一些（离线听）") { ListenService.setMore(!ListenService.state().more) }
            .also { col.addView(it, Ui.lp(this, 6f)) }

        textButton = Ui.button(this, "看文字") { openText() }.also { col.addView(it, Ui.lp(this, 8f)) }
        stopButton = Ui.text(this, "停止听书", 12f, Ui.INK2, center = true).apply {
            val v = Ui.dp(this@ListenActivity, 6f)
            setPadding(v * 3, v, v * 3, v)
            setOnClickListener {
                ListenService.stopNow()
                finish()
            }
        }
        col.addView(stopButton, Ui.lp(this, 4f, matchWidth = false))
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
            volumeRow.visibility = View.GONE
            options.visibility = View.GONE
            offlineView.visibility = View.GONE
            moreButton.visibility = View.GONE
            stopButton.visibility = View.GONE
            textButton.text = "返回"
            return
        }
        controls.visibility = View.VISIBLE
        volumeRow.visibility = View.VISIBLE
        updateVolume()
        options.visibility = View.VISIBLE
        moreButton.visibility = View.VISIBLE
        stopButton.visibility = View.VISIBLE
        textButton.text = "看文字"
        titleView.text = s.title
        chapterView.text = s.chapter
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
        playButton.setImageResource(if (s.playing) android.R.drawable.ic_media_pause else android.R.drawable.ic_media_play)
        playButton.contentDescription = if (s.playing) "暂停" else "继续"
        speedButton.text = String.format(Locale.US, "%.1f×", s.speed)
        timerButton.text = when {
            s.timer == -1 -> "读完本章停"
            s.timer > 0 -> "还剩 ${ceil(s.timerLeftMs / 60_000.0).toInt()} 分"
            else -> "定时"
        }
        offlineView.text = offlineLine(s)
        offlineView.visibility = if (offlineView.text.isEmpty()) View.GONE else View.VISIBLE
        moreButton.text = if (s.more) "不用多准备了" else "多准备一些（离线听）"
    }

    private fun adjustVolume(dir: Int) {
        val am = getSystemService(AUDIO_SERVICE) as AudioManager
        am.adjustStreamVolume(AudioManager.STREAM_MUSIC, if (dir > 0) AudioManager.ADJUST_RAISE else AudioManager.ADJUST_LOWER, 0)
        updateVolume()
    }

    private fun updateVolume() {
        val am = getSystemService(AUDIO_SERVICE) as AudioManager
        val max = am.getStreamMaxVolume(AudioManager.STREAM_MUSIC)
        val cur = am.getStreamVolume(AudioManager.STREAM_MUSIC)
        volumeView.text = if (max > 0) "${cur * 100 / max}%" else ""
    }

    /** 离开手机、没网时还能听多久（平时自动准备几分钟；点了“多准备一些”准备约半小时） */
    private fun offlineLine(s: ListenService.State): String {
        val min = s.offlineSec / 60
        return when {
            !s.online -> if (min > 0) "没联网，准备好的还能听约 $min 分钟" else "没联网"
            s.more && s.preparing -> if (min > 0) "正在准备离线听（约半小时），已准备 $min 分钟" else "正在准备离线听（约半小时）…"
            min > 0 -> "已准备离线 $min 分钟"
            else -> ""
        }
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
        /** “继续听”：书的 ID，-1 表示 Tile 上现在显示的那本 */
        const val EXTRA_RESUME = "resume"
    }
}
