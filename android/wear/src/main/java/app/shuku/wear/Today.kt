package app.shuku.wear

import android.content.ComponentName
import android.os.SystemClock
import androidx.wear.tiles.TileService
import androidx.wear.watchface.complications.datasource.ComplicationDataSourceUpdateRequester
import org.json.JSONArray
import org.json.JSONObject
import java.time.LocalDate
import kotlin.math.max
import kotlin.math.roundToInt

/**
 * Tile 和表盘复杂功能显示的东西：今天读了多少分钟、正在读（听）哪本书。
 * 联网时取书库的 GET /api/stats/today（{"seconds":…,"book":{"id","title","percent"}}，按当前登录的人），存在手表上；
 * 之后手表上读、听的时长先在本地累加，连不上书库时也能看到。书：手表正在听的 > 书库说的和手表上最近打开的里面更新的那本 > “在读”列表第一本。
 */
object Today {
    class Book(val id: Long, val title: String, val percent: Double)
    class Info(val configured: Boolean, val seconds: Int, val book: Book?, val listening: Boolean, val playing: Boolean)

    private val app get() = WearApp.app
    private val prefs get() = WearApp.app.config.prefs

    @Volatile private var refreshing = false
    @Volatile private var lastTry = 0L // 上次去问书库的时间（elapsedRealtime）
    @Volatile private var fails = 0 // 连着失败了几次：越失败隔得越久，不会一直重试
    @Volatile private var missingUntil = 0L

    private fun day() = LocalDate.now().toString()

    private fun obj(key: String): JSONObject? = prefs.getString(key, null)?.let { runCatching { JSONObject(it) }.getOrNull() }

    /** 今天读了多少秒：书库上次说的 + 那之后手表上读、听的 */
    private fun seconds(): Int {
        val today = day()
        var s = 0
        obj("today_stats")?.let { if (it.optString("day") == today) s += it.optInt("seconds") }
        obj("today_local")?.let { if (it.optString("day") == today) s += it.optInt("seconds") }
        return s
    }

    /** 现在要显示的（主线程、后台线程都能调用） */
    fun info(): Info {
        val now = ListenService.now
        val stats = obj("today_stats")
        val last = obj("last_book")?.takeIf { it.optLong("id") > 0 }
        val server = stats?.optJSONObject("book")?.takeIf { it.optLong("id") > 0 }
        val book = when {
            now != null -> Book(now.bookId, now.title, now.percent)
            server != null && (last == null || last.optLong("at") <= stats.optLong("at")) ->
                Book(server.optLong("id"), server.optString("title"), server.optDouble("percent", -1.0))
            last != null -> Book(last.optLong("id"), last.optString("title"), last.optDouble("percent", -1.0))
            else -> firstReading()
        }
        return Info(app.config.configured, seconds(), book, now != null, now?.playing == true)
    }

    // 首页“在读”列表里的第一本（漫画在手表上看不了，跳过）
    private fun firstReading(): Book? {
        val items = runCatching { JSONArray(app.config.str("reading_cache", "")) }.getOrNull() ?: return null
        for (i in 0 until items.length()) {
            val b = items.optJSONObject(i) ?: continue
            if (b.optString("kind") == "comic" || b.optLong("id") <= 0) continue
            return Book(b.optLong("id"), b.optString("title"), b.optDouble("percent", -1.0))
        }
        return null
    }

    // 显示的内容有没有变（分钟数、书、进度）
    private fun signature(): String {
        val i = info()
        val pct = i.book?.percent?.let { if (it >= 0) (it * 100).roundToInt() else -1 }
        return "${i.configured}|${i.seconds / 60}|${i.book?.id}|${i.book?.title}|$pct|${i.listening}|${i.playing}"
    }

    /** 手表上读了、听了 n 秒（存进度时调用，不管传没传上去） */
    fun addSeconds(n: Int) {
        if (n <= 0) return
        val changed = synchronized(this) {
            val today = day()
            val before = seconds() / 60
            val cur = obj("today_local")?.takeIf { it.optString("day") == today }?.optInt("seconds") ?: 0
            prefs.edit().putString("today_local", JSONObject().put("day", today).put("seconds", cur + n).toString()).apply()
            seconds() / 60 != before
        }
        if (changed) updateViews(urgent = false)
    }

    /** 手表上打开、听了这本书（percent 不知道时传 -1） */
    fun noteBook(id: Long, title: String, percent: Double = -1.0) {
        if (id <= 0) return
        val urgent = synchronized(this) {
            val old = obj("last_book")?.takeIf { it.optLong("id") == id }
            val oldPct = old?.optDouble("percent", -1.0) ?: -1.0
            val p = if (percent >= 0) percent else oldPct
            val t = title.ifEmpty { old?.optString("title") ?: "" }
            prefs.edit().putString("last_book", JSONObject().put("id", id).put("title", t).put("percent", p)
                .put("at", System.currentTimeMillis()).toString()).apply()
            when {
                old == null -> true // 换了一本书
                (oldPct * 100).roundToInt() != (p * 100).roundToInt() || old.optString("title") != t -> false
                else -> null // 显示的没变
            }
        } ?: return
        updateViews(urgent)
    }

    /** 断开书库了 */
    fun clear() {
        prefs.edit().remove("today_stats").remove("today_local").remove("last_book").apply()
        updateViews()
    }

    /**
     * 后台问一次书库的今日统计，显示的内容变了就刷新 Tile 和表盘。省电：Tile、表盘显示时先用手表上存的，
     * 存的不到半小时就不问（force：用户打开了首页，最多两分钟一次）；没网、没连书库时不问；
     * 失败了隔 15、30、60 分钟再试，不会重试个没完。旧版书库没有这个接口（404）：半小时内不再问，只用手表上的数据。
     */
    fun refresh(force: Boolean = false) {
        val a = app
        if (!a.config.configured || refreshing) return
        val now = SystemClock.elapsedRealtime()
        val gap = when {
            force -> 2 * 60_000L // 用户刚打开首页、首页刚连上书库
            fails > 0 -> 15 * 60_000L shl minOf(fails - 1, 2)
            else -> 30 * 60_000L
        }
        if (lastTry > 0 && now - lastTry < gap) return
        // 进程刚起来（lastTry 是 0）：看存的数据有多新
        if (!force && System.currentTimeMillis() - (obj("today_stats")?.optLong("at") ?: 0L) < 30 * 60_000L) return
        if (now < missingUntil || !a.netUsable()) return
        refreshing = true
        lastTry = now
        a.io.execute {
            try {
                val r = a.api("GET", "/api/stats/today", timeoutMs = 8000)
                when {
                    r.code == 404 -> missingUntil = SystemClock.elapsedRealtime() + 30 * 60_000L
                    r.ok -> {
                        save(JSONObject(r.text))
                        fails = 0
                    }
                    else -> fails++
                }
            } catch (e: Exception) {
                fails++
            } finally {
                refreshing = false
            }
        }
    }

    private fun save(j: JSONObject) {
        val today = day()
        val rec = JSONObject().put("day", today).put("seconds", max(0, j.optInt("seconds"))).put("at", System.currentTimeMillis())
        j.optJSONObject("book")?.takeIf { it.optLong("id") > 0 }?.let { b ->
            rec.put("book", JSONObject().put("id", b.optLong("id")).put("title", b.optString("title")).put("percent", b.optDouble("percent", -1.0)))
        }
        val changed = synchronized(this) {
            val before = signature()
            // 书库的数字里已经有传上去的时长；还没传上去的（离线时记下的）接着在本地算
            prefs.edit().putString("today_stats", rec.toString())
                .putString("today_local", JSONObject().put("day", today).put("seconds", app.pendingSeconds()).toString()).apply()
            signature() != before
        }
        if (changed) updateViews()
    }

    private var lastUpdate = 0L
    private var updatePending = false
    private val updateTask = Runnable {
        updatePending = false
        lastUpdate = SystemClock.elapsedRealtime()
        push()
    }

    /**
     * 请系统刷新 Tile 和表盘复杂功能（只在数据变了时调用，不定时刷新）。状态变了（换书、开始/暂停听书）urgent，
     * 十秒内合并成一次；只是分钟数、进度涨了，十分钟最多刷新一次，省电。
     */
    fun updateViews(urgent: Boolean = true) {
        val main = app.main
        main.post {
            if (updatePending && !urgent) return@post
            main.removeCallbacks(updateTask)
            val gap = if (urgent) 10_000L else 10 * 60_000L
            updatePending = true
            main.postDelayed(updateTask, max(0L, lastUpdate + gap - SystemClock.elapsedRealtime()))
        }
    }

    private fun push() {
        val a = app
        try {
            TileService.getUpdater(a).requestUpdate(ShukuTile::class.java)
        } catch (e: Exception) {
        }
        try {
            ComplicationDataSourceUpdateRequester.create(a, ComponentName(a, TodayComplication::class.java)).requestUpdateAll()
        } catch (e: Exception) {
        }
    }
}
