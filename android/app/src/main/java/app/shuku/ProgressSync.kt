package app.shuku

import org.json.JSONObject

/**
 * 阅读进度：先记在手机上，再同步到书库；连不上时排队，网络恢复后补传。
 * 同一本书排队期间只留最新的位置，但阅读时长（seconds）累加，统计不会少算。
 */
class ProgressSync(private val app: ShukuApp) {
    private val prefs = app.getSharedPreferences("progress_queue", 0)

    fun save(bookId: Int, body: String) {
        val j = try { JSONObject(body) } catch (e: Exception) { return }
        app.store.updateProgress(bookId, j)
        synchronized(this) {
            val old = prefs.getString(bookId.toString(), null)?.let { try { JSONObject(it) } catch (e: Exception) { null } }
            if (old != null) j.put("seconds", j.optInt("seconds") + old.optInt("seconds"))
            prefs.edit().putString(bookId.toString(), j.toString()).apply()
        }
        app.io.execute { flush() }
    }

    fun flush() {
        if (!app.link.online) return
        val pending = synchronized(this) { prefs.all.mapNotNull { (k, v) -> k.toIntOrNull()?.let { it to v.toString() } } }
        for ((id, body) in pending) {
            try {
                val r = app.link.send("PUT", "/api/books/$id/progress", JSONObject(body))
                if (r.ok || r.code in 400..499) {
                    synchronized(this) {
                        val now = prefs.getString(id.toString(), null)
                        if (now == body) prefs.edit().remove(id.toString()).apply()
                        else if (now != null) {
                            // 发送期间又有新的进度：留着下次发，但已经发过的阅读时长要减掉
                            val n = JSONObject(now)
                            n.put("seconds", maxOf(0, n.optInt("seconds") - JSONObject(body).optInt("seconds")))
                            prefs.edit().putString(id.toString(), n.toString()).apply()
                        }
                    }
                }
            } catch (e: Exception) {
                return
            }
        }
    }
}
