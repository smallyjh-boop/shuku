package app.shuku.core

import android.speech.tts.TextToSpeech
import android.speech.tts.Voice
import java.util.Locale

/**
 * 设备自带语音（手机、手表共用）：一句按 MixedText 切成几段，每段切换到那种语言的声音再读。
 * 设备上没有这种语言的声音时返回 false（不要拿英文声音硬读中文）。
 */
object DeviceSpeech {
    private var cacheFor: TextToSpeech? = null
    private var cached: List<Voice> = emptyList()

    /** 设备上的声音列表（每个 TextToSpeech 取一次，取声音列表要跨进程，很慢） */
    private fun voices(t: TextToSpeech): List<Voice> {
        if (cacheFor !== t) {
            cached = try { t.voices?.toList() } catch (e: Exception) { null } ?: emptyList()
            cacheFor = t
        }
        return cached
    }

    /** 换了引擎、装了新的语音包：下次重新取 */
    fun forget() { cacheFor = null; cached = emptyList() }

    /**
     * 把 t 切到 loc 这种语言（preferred：用户选过的音色名，是这种语言的才用）。
     * 返回 false 表示这台设备没有这种语言的声音
     */
    fun use(t: TextToSpeech, loc: Locale, preferred: String = ""): Boolean {
        val all = voices(t).filter { it.locale.language == loc.language }
        val installed = all.filter { !it.features.orEmpty().contains(TextToSpeech.Engine.KEY_FEATURE_NOT_INSTALLED) }
        val local = installed.filter { !it.isNetworkConnectionRequired }
        val pool = local.ifEmpty { installed }
        val pick = pool.firstOrNull { it.name == preferred }
            ?: pool.firstOrNull { it.locale.country.equals(loc.country, ignoreCase = true) }
            ?: pool.firstOrNull()
        if (pick != null) {
            if (t.voice?.name != pick.name) try { t.voice = pick } catch (e: Exception) {}
            return true
        }
        // 有的引擎不列声音：直接按语言设
        val r = try { t.setLanguage(loc) } catch (e: Exception) { TextToSpeech.LANG_NOT_SUPPORTED }
        return r >= TextToSpeech.LANG_AVAILABLE
    }
}
