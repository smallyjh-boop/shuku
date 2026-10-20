package app.shuku

import app.shuku.core.HttpResult
import org.json.JSONObject

/**
 * 阅读进度：先记在手机上，再同步到书库；连不上时排队，网络恢复后补传。
 * - 同一本书排队期间只留最后的位置，阅读时长（seconds）相加，最多 4 小时（书库一次最多收这么多）
 * - 网页传来的其他字段（base、at 等）原样转发
 * - 一次只传一条（同一时刻只有一个请求），不会把同一条传两次、把阅读时长算两遍
 * - 书库回 2xx 才从队列里删掉（回 conflict: true 也算：别的设备读得更新，书库留了那边的位置）；
 *   网络不通、书库出错（5xx）、没登录（401）等都留着下次再传。
 *   例外：书在书库上已经删了（404）直接丢掉；书库连续 7 天都不收的（不是没登录）也丢掉，免得每翻一页都重传一次
 */
class ProgressSync(private val app: ShukuApp) {
    private val prefs = app.getSharedPreferences("progress_queue", 0)
    /** 书库第一次拒收某本书的进度的时间（毫秒），传成功就清掉 */
    private val rejected = app.getSharedPreferences("progress_rejected", 0)
    private val lock = Any()
    private var running = false // 有线程正在上传
    private var again = false   // 上传期间又有新进度：这一轮传完接着再来一轮

    fun save(bookId: Int, body: String) {
        val j = try { JSONObject(body) } catch (e: Exception) { return }
        // time_only：位置没变、只报阅读时长（网页从冻结中醒来时手上的位置可能是旧的），不改本机记的位置
        val timeOnly = j.optBoolean("time_only")
        if (!timeOnly) try { app.store.updateProgress(bookId, j) } catch (e: Exception) {}
        synchronized(lock) {
            val old = prefs.getString(bookId.toString(), null)?.let { try { JSONObject(it) } catch (e: Exception) { null } }
            val secs = j.optInt("seconds").coerceAtLeast(0) + (old?.optInt("seconds") ?: 0).coerceAtLeast(0)
            // 排着队的是一个真正的位置：留着它，只把时长加上去
            val keep = if (timeOnly && old != null) old else j
            keep.put("seconds", secs.coerceAtMost(MAX_SECONDS))
            prefs.edit().putString(bookId.toString(), keep.toString()).apply()
        }
        app.io.execute { flush() }
    }

    /** 把排队的进度传给书库。任何线程都可以调用：已经有线程在传，就让它传完再来一轮，这里马上返回 */
    fun flush() {
        synchronized(lock) {
            if (running) {
                again = true
                return
            }
            running = true
            again = false
        }
        var more = true
        while (more) {
            val ok = try { sendAll() } catch (e: Throwable) { false }
            synchronized(lock) {
                more = ok && again
                again = false
                if (!more) running = false
            }
        }
    }

    /** 一条一条传；返回 false 表示网络不通，这一轮先停下 */
    private fun sendAll(): Boolean {
        if (!app.link.online) return false
        val ids = synchronized(lock) { prefs.all.keys.mapNotNull { it.toIntOrNull() } }
        for (id in ids) {
            val body = synchronized(lock) { prefs.getString(id.toString(), null) } ?: continue
            val json = try { JSONObject(body) } catch (e: Exception) { null }
            if (json == null) {
                done(id, body, JSONObject())
                continue
            }
            val r = try { app.link.send("PUT", "/api/books/$id/progress", json) } catch (e: Exception) { return false }
            when {
                r.ok -> {
                    done(id, body, json)
                    tellWeb(id, r)
                }
                // 书在书库上已经删了：丢掉
                r.code == 404 || r.code == 410 -> done(id, body, json)
                // 没登录、太频繁：留着，重新配对、过一会儿就能传
                r.code == 401 || r.code == 403 || r.code == 408 || r.code == 429 -> {}
                // 书库出错（5xx）、格式不对（400）等：留着下次再传，接着传别的书；连续 7 天都不收就丢掉
                else -> {
                    val now = System.currentTimeMillis()
                    val first = rejected.getLong(id.toString(), 0L)
                    if (first == 0L) rejected.edit().putLong(id.toString(), now).apply()
                    else if (now - first > GIVE_UP_MS) done(id, body, json)
                }
            }
        }
        return true
    }

    /** 这一条传完了：从队列里删掉。传的时候这本书又有了新进度：留着下次传，但已经传过的阅读时长要减掉 */
    private fun done(id: Int, body: String, sent: JSONObject) = synchronized(lock) {
        rejected.edit().remove(id.toString()).apply()
        val now = prefs.getString(id.toString(), null)
        if (now == body || now == null) {
            prefs.edit().remove(id.toString()).apply()
        } else {
            val n = try { JSONObject(now) } catch (e: Exception) { null }
            if (n == null) prefs.edit().remove(id.toString()).apply()
            else {
                n.put("seconds", maxOf(0, n.optInt("seconds") - sent.optInt("seconds")))
                prefs.edit().putString(id.toString(), n.toString()).apply()
            }
        }
    }

    /**
     * App 里进度是原生传的，网页看不到书库的回复：转成 progress 事件告诉网页
     * {"id": 书, "updated_at": …, "conflict": true, "current": {…}}（后两个只在别的设备读得更新时有）
     */
    private fun tellWeb(id: Int, r: HttpResult) {
        val j = try { JSONObject(r.text) } catch (e: Exception) { return }
        Events.emit("progress", j.put("id", id))
    }

    companion object {
        /** 一次最多报 4 小时的阅读时长（和书库的上限一样） */
        const val MAX_SECONDS = 14400

        private const val GIVE_UP_MS = 7 * 24 * 3600_000L
    }
}
