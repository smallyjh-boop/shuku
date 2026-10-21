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
import android.widget.FrameLayout
import android.widget.ImageView
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

    /**
     * 圆形图标按钮，下面一行小字。一排放三个：宽度按屏幕算（圆表盘中间那一段放得下三个，不会跑到屏幕外面）。
     * 图标用 res/drawable 里的 ic_a_*；也可以显示几个字（比如语速“1.2×”）
     */
    class Action(val root: LinearLayout, val circle: FrameLayout, private val img: ImageView, private val txt: TextView, val label: TextView, private val radius: Float) {
        fun icon(res: Int) {
            if (img.tag != res) {
                img.tag = res
                img.setImageResource(res)
            }
            img.visibility = View.VISIBLE
            txt.visibility = View.GONE
        }

        fun text(s: String) {
            txt.text = s
            txt.visibility = View.VISIBLE
            img.visibility = View.GONE
        }

        fun caption(s: String) {
            label.text = s
            label.visibility = if (s.isEmpty()) View.GONE else View.VISIBLE
        }

        /** 开着、主要的：石青底黑图标 */
        fun on(on: Boolean) {
            circle.background = pill(if (on) ACCENT else CARD, radius)
            img.setColorFilter(if (on) BG else INK)
            txt.setTextColor(if (on) BG else INK)
        }

        fun desc(s: String) {
            circle.contentDescription = s
        }
    }

    /** 一个按钮占多宽（dp）：三个一排放在页面中间能用的宽度里，最大 60 */
    fun actionWidth(ctx: Context): Float {
        val m = ctx.resources.displayMetrics
        val round = ctx.resources.configuration.isScreenRound
        val usable = m.widthPixels * (if (round) 0.76f else 0.9f) / m.density
        return minOf(60f, usable / 3f - 4f)
    }

    /** inCard：放在卡片里（卡片两边有留白，按钮小一点） */
    fun action(ctx: Context, icon: Int?, caption: String, primary: Boolean = false, inCard: Boolean = false, onClick: () -> Unit): Action {
        val w = actionWidth(ctx) - if (inCard) 9f else 0f
        val size = dp(ctx, w - 8f)
        val radius = size / 2f
        val img = ImageView(ctx).apply {
            val p = (size * 0.24f).roundToInt()
            setPadding(p, p, p, p)
            scaleType = ImageView.ScaleType.FIT_CENTER
        }
        val txt = text(ctx, "", 13f, INK, bold = true, center = true).apply { gravity = Gravity.CENTER }
        val circle = FrameLayout(ctx).apply {
            addView(img, FrameLayout.LayoutParams(size, size))
            addView(txt, FrameLayout.LayoutParams(size, size))
            isClickable = true
            isFocusable = true
            contentDescription = caption
            setOnClickListener { onClick() }
        }
        val label = text(ctx, caption, 10f, INK2, center = true, maxLines = 1)
        val root = LinearLayout(ctx).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            addView(circle, LinearLayout.LayoutParams(size, size))
            addView(label, LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(ctx, 3f) })
            layoutParams = LinearLayout.LayoutParams(dp(ctx, w), ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = dp(ctx, 2f); marginEnd = dp(ctx, 2f) }
        }
        val a = Action(root, circle, img, txt, label, radius)
        if (icon != null) a.icon(icon) else a.text("")
        a.caption(caption)
        a.on(primary)
        return a
    }

    /** 一排圆形按钮（居中） */
    fun actions(ctx: Context, vararg items: Action): LinearLayout = LinearLayout(ctx).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER
        for (it in items) addView(it.root)
    }

    fun lp(ctx: Context, top: Float = 0f, matchWidth: Boolean = true) = LinearLayout.LayoutParams(
        if (matchWidth) ViewGroup.LayoutParams.MATCH_PARENT else ViewGroup.LayoutParams.WRAP_CONTENT,
        ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(ctx, top) }

    /** 一整页：可以用表冠滚动的 ScrollView，圆表盘四周留白。overlay：外面再套一层 FrameLayout（sv.parent），可以放浮在上面的按钮 */
    fun page(a: Activity, overlay: Boolean = false): Pair<ScrollView, LinearLayout> {
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
        if (overlay) a.setContentView(FrameLayout(a).apply {
            setBackgroundColor(BG)
            addView(sv, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        }) else a.setContentView(sv)
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
