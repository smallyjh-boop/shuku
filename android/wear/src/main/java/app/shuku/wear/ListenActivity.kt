package app.shuku.wear

import android.Manifest
import android.app.Activity
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
 * 手表上的听书播放页：正在读的这一句、上一句 / 暂停 / 下一句、语速、定时、回到文字。
 * 没连耳机时提示去连（Wear OS 建议听东西用蓝牙耳机），手表有扬声器的话也可以选用扬声器。
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
    private lateinit var playButton: ImageButton
    private lateinit var options: LinearLayout
    private lateinit var speedButton: TextView
    private lateinit var timerButton: TextView
    private lateinit var textButton: TextView
    private lateinit var stopButton: TextView

    private val receiver = object : BroadcastReceiver() {
        override fun onReceive(c: Context, i: Intent) = update()
    }

    // 定时还剩几分钟：每半分钟刷新一次
    private val tick = object : Runnable {
        override fun run() {
            update()
            app.main.postDelayed(this, 30_000)
        }
    }

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
    }

    override fun onResume() {
        super.onResume()
        update()
        app.main.removeCallbacks(tick)
        app.main.postDelayed(tick, 30_000)
    }

    override fun onPause() {
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

        options = row()
        val gap = Ui.dp(this, 3f)
        speedButton = Ui.button(this, "1.0×") { ListenService.cycleSpeed() }
        timerButton = Ui.button(this, "定时") { ListenService.cycleTimer() }
        options.addView(speedButton, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginEnd = gap })
        options.addView(timerButton, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = gap })
        col.addView(options, Ui.lp(this, 8f, matchWidth = false))

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
        if (!s.active) {
            // 停了（读完了、在别处点了停止）
            titleView.text = "听书"
            chapterView.text = ""
            textView.text = s.status.ifEmpty { "没有在听的书。打开一本书，点“听书”。" }
            statusView.text = ""
            outputBox.visibility = View.GONE
            controls.visibility = View.GONE
            options.visibility = View.GONE
            stopButton.visibility = View.GONE
            textButton.text = "返回"
            return
        }
        controls.visibility = View.VISIBLE
        options.visibility = View.VISIBLE
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
}
