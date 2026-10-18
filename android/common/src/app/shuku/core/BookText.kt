package app.shuku.core

import org.json.JSONArray
import org.json.JSONObject
import java.util.Locale
import kotlin.math.max
import kotlin.math.min

// 手表和手机共用：书库按章给的正文切成朗读的句子、换算成阅读器认识的位置
// （手表的阅读和听书；手机后台听书在网页不在时自己从书库取后面的章节）

/** 显示的正文和朗读用的句子。阅读位置都按这段排好的文字算比例，读和听对得上 */
object BookText {
    /** 每段前空两格 */
    fun format(raw: String): String =
        raw.split('\n').map { it.trim() }.filter { it.isNotEmpty() }.joinToString("\n") { "　　$it" }

    class Sentence(val text: String, val start: Int, val end: Int)

    // 一句：到句末标点（连同后面的引号、括号）为止；没有标点的一段到换行为止
    private val partRe = Regex("[^。！？!?；;…\\n]*[。！？!?；;…]+[”’」』）)\"'》]*[ \\t\\u3000]*|[^。！？!?；;…\\n]+|\\n+")
    private val commaRe = Regex("[^，,、：:]*[，,、：:]+|[^，,、：:]+")

    /** 按句切开（和网页、手机听书一样）：太短的合并，太长的按逗号再切，合成更快 */
    fun sentences(text: String): List<Sentence> {
        val raw = ArrayList<Sentence>()
        val buf = StringBuilder()
        var start = -1
        var end = 0
        for (m in partRe.findAll(text)) {
            if (start < 0) start = m.range.first
            buf.append(m.value)
            end = m.range.last + 1
            val clean = buf.toString().trim()
            if (clean.length >= 24 || m.value.contains('\n')) {
                if (clean.isNotEmpty()) raw += Sentence(clean, start, end)
                buf.setLength(0)
                start = -1
            }
        }
        buf.toString().trim().let { if (it.isNotEmpty()) raw += Sentence(it, max(0, start), end) }
        val out = ArrayList<Sentence>()
        for (s in raw) {
            if (s.text.length <= 180) {
                out += s
                continue
            }
            var off = s.start
            val piece = StringBuilder()
            fun flush(endAt: Int) {
                // 没有逗号的长段：每 120 个字左右切一刀，有空格就在空格处切（泰文用空格隔开句子）
                var p = piece.toString()
                while (p.length > 200) {
                    val sp = p.lastIndexOf(' ', 160).takeIf { it >= 60 } ?: -1
                    val cut = if (sp > 0) sp + 1 else 120
                    out += Sentence(p.substring(0, cut).trim(), off, off + cut)
                    off += cut
                    p = p.substring(cut)
                }
                if (p.isNotBlank()) out += Sentence(p.trim(), off, endAt)
                off = endAt
                piece.setLength(0)
            }
            for (m in commaRe.findAll(s.text)) {
                piece.append(m.value)
                if (piece.length >= 90) flush(min(s.end, off + piece.length))
            }
            if (piece.isNotEmpty()) flush(s.end)
        }
        return out
    }
}

/** 换算成网页阅读器认识的位置，手机、网页能接着读 */
object BookPos {
    /** 第 cur 章里比例 frac 处：sec:节:比例（EPUB、TXT 等）/ p:页:0（PDF）；MOBI、FB2 等整本一段文字的格式返回空，只能按百分比 */
    fun location(chapters: JSONArray, cur: Int, frac: Double): String {
        val loc = chapters.optJSONObject(cur)?.optString("loc") ?: return ""
        val next = chapters.optJSONObject(cur + 1)?.optString("loc") ?: ""
        if (loc.startsWith("sec:")) {
            // 一节太长时拆成了几章：loc 里的比例是这一章在整节里的起点，下一章的起点就是终点
            val p = loc.split(":")
            val sec = p.getOrNull(1) ?: return loc
            val f0 = p.getOrNull(2)?.toDoubleOrNull() ?: 0.0
            val np = next.split(":")
            val f1 = if (next.startsWith("sec:") && np.getOrNull(1) == sec) np.getOrNull(2)?.toDoubleOrNull() ?: 1.0 else 1.0
            return "sec:$sec:" + String.format(Locale.US, "%.4f", (f0 + frac * (f1 - f0)).coerceIn(0.0, 1.0))
        }
        if (loc.startsWith("p:")) {
            // PDF 几页合成一章：按比例估算在第几页
            val first = loc.split(":").getOrNull(1)?.toIntOrNull() ?: return loc
            val nextFirst = if (next.startsWith("p:")) next.split(":").getOrNull(1)?.toIntOrNull() else null
            val page = if (nextFirst != null && nextFirst > first) first + min((frac * (nextFirst - first)).toInt(), nextFirst - first - 1) else first
            return "p:$page:0"
        }
        return ""
    }

    /** 全书的百分比。ch 是这一章的正文（带 start、chars、total） */
    fun percent(ch: JSONObject, frac: Double, done: Boolean): Double {
        if (done) return 1.0
        val total = max(1, ch.optInt("total"))
        return ((ch.optInt("start") + frac * ch.optInt("chars")) / total).coerceIn(0.0, 1.0)
    }
}
