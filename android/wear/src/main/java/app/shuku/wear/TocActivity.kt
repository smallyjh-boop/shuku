package app.shuku.wear

import android.app.Activity
import android.content.Intent
import android.graphics.Typeface
import android.os.Bundle
import android.view.InputDevice
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.ViewGroup
import android.widget.BaseAdapter
import android.widget.ListView
import android.widget.TextView
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import kotlin.math.roundToInt

/** 目录：章节可能有上千个，用 ListView 只画看得见的几行。点一章回到阅读页 */
class TocActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val id = intent.getLongExtra("id", 0)
        val current = intent.getIntExtra("current", 0)
        // 阅读页刚取过目录，手表上一定存着
        val arr = runCatching { JSONObject(File(cacheDir, "books/$id/toc.json").readText()).optJSONArray("chapters") }.getOrNull() ?: JSONArray()
        val titles = (0 until arr.length()).map { arr.optJSONObject(it)?.optString("title").orEmpty().ifEmpty { "第 ${it + 1} 节" } }
        val round = resources.configuration.isScreenRound
        val m = resources.displayMetrics
        val pad = Ui.dp(this, 9f)
        val lv = ListView(this).apply {
            setBackgroundColor(Ui.BG)
            divider = null
            val side = if (round) (m.widthPixels * 0.12f).roundToInt() else Ui.dp(this@TocActivity, 8f)
            setPadding(side, (m.heightPixels * 0.2f).roundToInt(), side, (m.heightPixels * 0.3f).roundToInt())
            clipToPadding = false
            isVerticalScrollBarEnabled = false
            isFocusable = true
            isFocusableInTouchMode = true
            adapter = object : BaseAdapter() {
                override fun getCount() = titles.size
                override fun getItem(position: Int) = titles[position]
                override fun getItemId(position: Int) = position.toLong()
                override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
                    val tv = convertView as? TextView ?: Ui.text(this@TocActivity, "", 14f, maxLines = 2).apply { setPadding(0, pad, 0, pad) }
                    tv.text = titles[position]
                    tv.setTextColor(if (position == current) Ui.ACCENT else Ui.INK)
                    tv.typeface = Typeface.create(Typeface.DEFAULT, if (position == current) Typeface.BOLD else Typeface.NORMAL)
                    return tv
                }
            }
            setOnItemClickListener { _, _, position, _ ->
                setResult(RESULT_OK, Intent().putExtra("i", position))
                finish()
            }
            // 表冠滚动
            setOnGenericMotionListener { v, ev ->
                if (ev.action == MotionEvent.ACTION_SCROLL && ev.isFromSource(InputDevice.SOURCE_ROTARY_ENCODER)) {
                    val d = -ev.getAxisValue(MotionEvent.AXIS_SCROLL) * ViewConfiguration.get(v.context).scaledVerticalScrollFactor
                    (v as ListView).scrollListBy(d.roundToInt())
                    true
                } else false
            }
        }
        setContentView(lv)
        lv.requestFocus()
        if (current in titles.indices) lv.setSelectionFromTop(current, (m.heightPixels * 0.35f).roundToInt())
    }
}
