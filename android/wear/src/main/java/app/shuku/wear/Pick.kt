package app.shuku.wear

import android.app.Activity
import android.app.Dialog
import android.content.res.ColorStateList
import android.view.Gravity
import android.view.InputDevice
import android.view.MotionEvent
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.SeekBar
import kotlin.math.roundToInt
import kotlin.math.sign

/** 手表上弹出来选：整屏的小页面（选语速、拖到第几章、更多操作） */
object Pick {
    private fun sheet(a: Activity): Pair<Dialog, LinearLayout> {
        val d = Dialog(a, android.R.style.Theme_DeviceDefault_NoActionBar)
        val m = a.resources.displayMetrics
        val round = a.resources.configuration.isScreenRound
        val col = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            val side = if (round) (m.widthPixels * 0.14f).roundToInt() else Ui.dp(a, 12f)
            val top = if (round) (m.heightPixels * 0.14f).roundToInt() else Ui.dp(a, 12f)
            setPadding(side, top, side, top + Ui.dp(a, 12f))
        }
        val sv = ScrollView(a).apply {
            setBackgroundColor(Ui.BG)
            isFocusable = true
            isFocusableInTouchMode = true
            addView(col)
        }
        Ui.rotary(sv)
        d.setContentView(sv, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        return d to col
    }

    /** 从几项里选一项（选中的那项亮着） */
    fun choose(a: Activity, title: String, items: List<String>, selected: Int, onPick: (Int) -> Unit) {
        val (d, col) = sheet(a)
        col.addView(Ui.text(a, title, 13f, Ui.ACCENT, bold = true, center = true), Ui.lp(a))
        items.forEachIndexed { i, s ->
            col.addView(Ui.button(a, s, primary = i == selected) {
                d.dismiss()
                onPick(i)
            }, Ui.lp(a, 6f))
        }
        d.show()
    }

    /**
     * 拖到第几项（第几章）：拖动或者转表冠（一格一项），上面显示是哪一章，点“跳到这里”才跳。
     * label：第 i 项显示什么
     */
    fun seek(a: Activity, title: String, count: Int, cur: Int, label: (Int) -> String, onPick: (Int) -> Unit) {
        if (count <= 0) return
        val (d, col) = sheet(a)
        col.addView(Ui.text(a, title, 13f, Ui.ACCENT, bold = true, center = true), Ui.lp(a))
        val start = cur.coerceIn(0, count - 1)
        val lab = Ui.text(a, label(start), 14f, center = true, maxLines = 3)
        col.addView(lab, Ui.lp(a, 8f))
        val bar = SeekBar(a).apply {
            max = count - 1
            progress = start
            progressTintList = ColorStateList.valueOf(Ui.ACCENT)
            thumbTintList = ColorStateList.valueOf(Ui.ACCENT)
            progressBackgroundTintList = ColorStateList.valueOf(Ui.INK2)
            isFocusable = true
            isFocusableInTouchMode = true
            setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
                override fun onProgressChanged(s: SeekBar, p: Int, fromUser: Boolean) { lab.text = label(p) }
                override fun onStartTrackingTouch(s: SeekBar) {}
                override fun onStopTrackingTouch(s: SeekBar) {}
            })
            // 表冠：一格一项
            setOnGenericMotionListener { v, ev ->
                if (ev.action == MotionEvent.ACTION_SCROLL && ev.isFromSource(InputDevice.SOURCE_ROTARY_ENCODER)) {
                    val s = -ev.getAxisValue(MotionEvent.AXIS_SCROLL).sign.toInt()
                    (v as SeekBar).progress = (v.progress + s).coerceIn(0, v.max)
                    true
                } else false
            }
        }
        col.addView(bar, Ui.lp(a, 10f))
        col.addView(Ui.button(a, "跳到这里", primary = true) {
            d.dismiss()
            if (bar.progress != start) onPick(bar.progress)
        }, Ui.lp(a, 12f))
        col.addView(Ui.button(a, "取消") { d.dismiss() }, Ui.lp(a, 6f))
        d.show()
        bar.requestFocus()
    }
}
