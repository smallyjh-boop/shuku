package app.shuku.wear

import android.app.Activity
import android.content.Context
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.text.TextUtils
import android.view.Gravity
import android.view.InputDevice
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.ViewGroup
import android.widget.ImageButton
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import kotlin.math.roundToInt

/** 手表界面：纯黑底（OLED 省电）、暖白字、石青色点缀；圆形表盘四周多留白 */
object Ui {
    const val BG = 0xFF000000.toInt()
    const val INK = 0xFFE4DED2.toInt()
    const val INK2 = 0xFF9A968E.toInt()
    const val ACCENT = 0xFF86B6CB.toInt()
    const val CARD = 0xFF16191E.toInt()
    const val DANGER = 0xFFE07C7C.toInt()

    fun dp(ctx: Context, v: Float): Int = (v * ctx.resources.displayMetrics.density).roundToInt()

    fun text(ctx: Context, s: CharSequence, sizeSp: Float, color: Int = INK, bold: Boolean = false, serif: Boolean = false, center: Boolean = false, maxLines: Int = 0): TextView =
        TextView(ctx).apply {
            text = s
            textSize = sizeSp
            setTextColor(color)
            typeface = Typeface.create(if (serif) Typeface.SERIF else Typeface.DEFAULT, if (bold) Typeface.BOLD else Typeface.NORMAL)
            if (center) gravity = Gravity.CENTER_HORIZONTAL
            if (maxLines > 0) {
                this.maxLines = maxLines
                ellipsize = TextUtils.TruncateAt.END
            }
        }

    fun pill(color: Int, radius: Float) = GradientDrawable().apply {
        setColor(color)
        cornerRadius = radius
    }

    fun button(ctx: Context, label: String, primary: Boolean = false, onClick: () -> Unit): TextView = text(ctx, label, 14f, if (primary) BG else INK, bold = primary, center = true).apply {
        background = pill(if (primary) ACCENT else CARD, dp(ctx, 22f).toFloat())
        val pad = dp(ctx, 10f)
        setPadding(dp(ctx, 16f), pad, dp(ctx, 16f), pad)
        isClickable = true
        isFocusable = true
        setOnClickListener { onClick() }
    }

    fun iconButton(ctx: Context, icon: Int, desc: String, onClick: () -> Unit): ImageButton = ImageButton(ctx).apply {
        setImageResource(icon)
        contentDescription = desc
        background = pill(CARD, dp(ctx, 22f).toFloat())
        setColorFilter(INK)
        val s = dp(ctx, 44f)
        layoutParams = LinearLayout.LayoutParams(s, s).apply { marginStart = dp(ctx, 4f); marginEnd = dp(ctx, 4f) }
        setOnClickListener { onClick() }
    }

    fun lp(ctx: Context, top: Float = 0f, matchWidth: Boolean = true) = LinearLayout.LayoutParams(
        if (matchWidth) ViewGroup.LayoutParams.MATCH_PARENT else ViewGroup.LayoutParams.WRAP_CONTENT,
        ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(ctx, top) }

    /** 一整页：可以用表冠滚动的 ScrollView，圆表盘四周留白 */
    fun page(a: Activity): Pair<ScrollView, LinearLayout> {
        val round = a.resources.configuration.isScreenRound
        val m = a.resources.displayMetrics
        val col = LinearLayout(a).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            val side = if (round) (m.widthPixels * 0.12f).roundToInt() else dp(a, 10f)
            setPadding(side, if (round) (m.heightPixels * 0.12f).roundToInt() else dp(a, 12f), side, if (round) (m.heightPixels * 0.2f).roundToInt() else dp(a, 24f))
        }
        val sv = ScrollView(a).apply {
            setBackgroundColor(BG)
            isVerticalScrollBarEnabled = true
            isFocusable = true
            isFocusableInTouchMode = true
            addView(col)
        }
        rotary(sv)
        a.setContentView(sv)
        sv.requestFocus()
        return sv to col
    }

    /** 表冠（旋转输入）滚动 */
    fun rotary(sv: ScrollView) {
        sv.setOnGenericMotionListener { v, ev ->
            if (ev.action == MotionEvent.ACTION_SCROLL && ev.isFromSource(InputDevice.SOURCE_ROTARY_ENCODER)) {
                val d = -ev.getAxisValue(MotionEvent.AXIS_SCROLL) * ViewConfiguration.get(v.context).scaledVerticalScrollFactor
                (v as ScrollView).scrollBy(0, d.roundToInt())
                true
            } else false
        }
    }

    fun divider(ctx: Context): View = View(ctx).apply {
        setBackgroundColor(0x22FFFFFF)
        layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 1).apply { topMargin = dp(ctx, 8f); bottomMargin = dp(ctx, 8f) }
    }
}
