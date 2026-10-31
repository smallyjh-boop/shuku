package app.shuku.core

import java.util.Locale

/**
 * 本机语音（手机、手表自带的文字转语音）读混着几种文字的句子：按文字种类切成几段，每段用那种语言的声音读。
 * 设备自带的语音一次只会一种语言，以前整句按主要的文字挑一种，中文句子里的英文、英文书里的中文就读不出来
 * （有的设备没有中文语音时，整句都用英文声音读）。
 *
 * 夹在中文、日文里的短英文（几个字母、一两个词）不单独切出来：中文声音读得了，切开反而一顿一顿的。
 * 数字、标点、空格跟着前面那段。
 */
object MixedText {
    class Run(val text: String, val locale: Locale)

    private enum class S { NONE, ZH, JA, KO, TH, RU, AR, LATIN }

    private fun kind(c: Char): S = when (Character.UnicodeScript.of(c.code)) {
        Character.UnicodeScript.HAN -> S.ZH
        Character.UnicodeScript.HIRAGANA, Character.UnicodeScript.KATAKANA -> S.JA
        Character.UnicodeScript.HANGUL -> S.KO
        Character.UnicodeScript.THAI -> S.TH
        Character.UnicodeScript.CYRILLIC -> S.RU
        Character.UnicodeScript.ARABIC -> S.AR
        Character.UnicodeScript.LATIN -> S.LATIN
        else -> S.NONE
    }

    private val NON_LATIN = setOf("zh", "ja", "ko", "th", "ru", "uk", "bg", "ar", "fa", "he", "hi", "el")

    /** bookLang：书的语言（zh、en、fr…），拉丁字母按它读（不是拉丁字母的语言就用英语） */
    fun runs(text: String, bookLang: String = ""): List<Run> {
        if (text.isBlank()) return emptyList()
        val hasKana = text.any { kind(it) == S.JA }
        val hasHangul = text.any { kind(it) == S.KO }
        // 日文里的汉字按日文读，韩文里的汉字按韩文读
        fun norm(s: S) = if (s == S.ZH && hasKana) S.JA else if (s == S.ZH && hasHangul && !hasKana) S.KO else s

        // 先按文字种类切开（数字标点跟着前一段；开头的跟着后一段）
        val parts = ArrayList<Pair<S, StringBuilder>>()
        for (c in text) {
            val k = norm(kind(c))
            if (k == S.NONE || (parts.isNotEmpty() && parts.last().first == k)) {
                if (parts.isEmpty()) parts += S.NONE to StringBuilder()
                parts.last().second.append(c)
            } else {
                if (parts.size == 1 && parts[0].first == S.NONE) parts[0] = k to parts[0].second.append(c)
                else parts += k to StringBuilder().append(c)
            }
        }
        // 夹在中日韩文字中间（或挨着）的短英文并到旁边那段：中文声音读得了几个英文单词。
        // 只在这句主要是中日韩文字时（一个字按 4 个字母算）；英文句子里夹的中文词，英文照样用英文声音读
        val cjk = setOf(S.ZH, S.JA, S.KO)
        val cjkWeight = text.count { norm(kind(it)) in cjk } * 4
        val latinWeight = text.count { kind(it) == S.LATIN }
        val mostlyCjk = cjkWeight >= latinWeight
        var i = 0
        while (i < parts.size) {
            val (k, sb) = parts[i]
            val letters = sb.count { it.isLetter() }
            val words = sb.trim().split(Regex("\\s+")).count { it.any(Char::isLetter) }
            val near = listOfNotNull(parts.getOrNull(i - 1)?.first, parts.getOrNull(i + 1)?.first).firstOrNull { it in cjk }
            if (k == S.LATIN && mostlyCjk && near != null && (letters <= 12 || words <= 2) && parts.size > 1) {
                if (i > 0 && parts[i - 1].first in cjk) { parts[i - 1].second.append(sb); parts.removeAt(i); continue }
                parts[i + 1] = parts[i + 1].first to StringBuilder(sb).append(parts[i + 1].second)
                parts.removeAt(i)
                continue
            }
            i++
        }
        // 合并后相邻同种的再连起来
        val merged = ArrayList<Pair<S, StringBuilder>>()
        for (p in parts) {
            if (merged.isNotEmpty() && merged.last().first == p.first) merged.last().second.append(p.second) else merged += p
        }
        val latin = bookLang.lowercase().substringBefore('-').let { if (it.length in 2..3 && it !in NON_LATIN) it else "en" }
        return merged.filter { it.second.isNotBlank() }.map { (k, sb) -> Run(sb.toString().trim(), localeOf(k, latin)) }
    }

    private fun localeOf(k: S, latin: String): Locale = when (k) {
        S.ZH -> Locale.SIMPLIFIED_CHINESE
        S.JA -> Locale.JAPAN
        S.KO -> Locale.KOREA
        S.TH -> Locale("th", "TH")
        S.RU -> Locale("ru", "RU")
        S.AR -> Locale("ar")
        S.LATIN, S.NONE -> if (latin == "en") Locale.US else Locale(latin)
    }

    /** 语言的中文名（提示“手机没有某某语音”用） */
    fun langName(l: Locale): String = when (l.language) {
        "zh" -> "中文"; "ja" -> "日语"; "ko" -> "韩语"; "th" -> "泰语"; "ru" -> "俄语"; "ar" -> "阿拉伯语"; "en" -> "英语"
        "fr" -> "法语"; "de" -> "德语"; "es" -> "西班牙语"; "it" -> "意大利语"; "pt" -> "葡萄牙语"; "vi" -> "越南语"
        else -> l.getDisplayLanguage(Locale.CHINA)
    }
}
