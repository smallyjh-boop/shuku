package app.shuku.wear

import android.app.Activity
import android.net.Uri
import android.os.Bundle
import android.text.InputType
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import org.json.JSONArray

/** 找书：搜索书名、作者（可以用手表的语音输入），没搜索时显示最近加入的书 */
class BooksActivity : Activity() {
    private val app get() = WearApp.app
    private lateinit var box: LinearLayout
    private lateinit var title: TextView
    private var seq = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val (_, col) = Ui.page(this)
        val q = EditText(this).apply {
            hint = "搜索书名或作者"
            textSize = 14f
            setTextColor(Ui.INK)
            setHintTextColor(Ui.INK2)
            isSingleLine = true
            inputType = InputType.TYPE_CLASS_TEXT
            imeOptions = EditorInfo.IME_ACTION_SEARCH
            setOnEditorActionListener { v, _, _ ->
                load(v.text.toString().trim())
                false
            }
        }
        col.addView(q, Ui.lp(this))
        title = Ui.text(this, "最近加入", 13f, Ui.ACCENT, bold = true)
        col.addView(title, Ui.lp(this, 8f))
        box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(box, Ui.lp(this, 2f))
        load("")
    }

    private fun load(query: String) {
        val my = ++seq
        title.text = if (query.isEmpty()) "最近加入" else "搜索“$query”"
        box.removeAllViews()
        box.addView(Ui.text(this, "正在加载…", 12f, Ui.INK2))
        val path = if (query.isEmpty()) "/api/books?sort=added&limit=30" else "/api/books?limit=30&q=" + Uri.encode(query)
        app.io.execute {
            val r = runCatching { app.json(path) }
            runOnUiThread {
                if (isDestroyed || my != seq) return@runOnUiThread
                box.removeAllViews()
                val e = r.exceptionOrNull()
                if (e != null) {
                    box.addView(Ui.text(this, e.message ?: WearApp.OFFLINE, 12f, Ui.DANGER))
                    return@runOnUiThread
                }
                val items = r.getOrThrow().optJSONArray("items") ?: JSONArray()
                var n = 0
                for (i in 0 until items.length()) {
                    val b = items.optJSONObject(i) ?: continue
                    if (b.optString("kind") == "comic") continue
                    box.addView(MainActivity.bookRow(this, b), Ui.lp(this, 6f))
                    n++
                }
                if (n == 0) box.addView(Ui.text(this, if (query.isEmpty()) "书库里还没有书" else "没有找到", 12f, Ui.INK2))
            }
        }
    }
}
