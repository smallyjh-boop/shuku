package app.shuku.wear

import android.app.Activity
import android.os.Bundle
import android.text.InputType
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.LinearLayout

/** 手动连接：手表直接填书库地址和配对码（手机不在身边、或没装手机 App 时用） */
class ConnectActivity : Activity() {
    private val app get() = WearApp.app

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val (_, col) = Ui.page(this)
        col.addView(Ui.text(this, "手动连接", 16f, bold = true, center = true))
        val addr = edit(col, "书库地址，如 192.168.1.10:8080", app.config.lan.ifEmpty { app.config.remote }.removePrefix("http://"),
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI)
        val code = edit(col, "配对码", "", InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_CAP_CHARACTERS or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS)
        col.addView(Ui.text(this, "配对码在网页的“设置 → 手机、手表和浏览器插件 → 显示配对二维码”里，10 分钟内有效。", 11f, Ui.INK2, center = true), Ui.lp(this, 6f))
        val status = Ui.text(this, "", 12f, Ui.DANGER, center = true)
        col.addView(Ui.button(this, "连接", primary = true) {
            val a = addr.text.toString()
            val k = code.text.toString().trim().uppercase()
            if (a.isBlank() || k.isEmpty()) {
                status.setTextColor(Ui.DANGER)
                status.text = "请填写地址和配对码"
                return@button
            }
            status.setTextColor(Ui.INK2)
            status.text = "正在连接…"
            app.io.execute {
                val err = app.link.connect(a, k, "", WearApp.DEVICE)
                runOnUiThread {
                    if (isDestroyed) return@runOnUiThread
                    if (err == null) {
                        app.config.prefs.edit().remove("connect_error").apply()
                        finish() // 回到首页，首页会刷新
                    } else {
                        status.setTextColor(Ui.DANGER)
                        status.text = err
                    }
                }
            }
        }, Ui.lp(this, 10f))
        col.addView(status, Ui.lp(this, 6f))
    }

    private fun edit(col: LinearLayout, hint: String, value: String, type: Int): EditText = EditText(this).apply {
        this.hint = hint
        setText(value)
        inputType = type
        textSize = 14f
        setTextColor(Ui.INK)
        setHintTextColor(Ui.INK2)
        isSingleLine = true
        imeOptions = EditorInfo.IME_ACTION_DONE
        col.addView(this, Ui.lp(this@ConnectActivity, 8f))
    }
}
