package app.shuku

import android.os.SystemClock
import android.widget.Toast
import app.shuku.core.Http
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

/**
 * 把书下载到手机：书的文件（TXT 等用服务器转好的 EPUB）、漫画的每一页、封面、进度、书签。
 * 一次下一本，排队的依次下载；下载期间开着前台服务（TransferService），切到后台、熄屏也不会停。
 * 每本书的下载状态记在它文件夹里的 state.json：
 *   downloading  正在下载（"file" 是书的文件已经下好时的地址，重试时用来判断要不要重下）
 *   error        下载失败或中断（"error" 是原因）：列表里显示“下载失败”，重试时已经下好的页、文件跳过
 *   done         下载完了（"at" 下载完的时间，毫秒；"size" 占的空间；"read_state" 下载时的阅读状态）
 *   removed      下载中被删掉：由下载线程收尾删文件夹，万一没来得及，下次打开 App 时删
 * 写盘出的错都在下载线程里接住，不会让 App 闪退。
 */
class Downloads(private val app: ShukuApp) {
    /** 一次下载任务。删除时只做标记（cancelled），由下载线程自己收尾 */
    private class Task(val id: Int, val auto: Boolean, val job: String) {
        @Volatile var cancelled = false
        @Volatile var title = ""
        @Volatile var author = ""
        @Volatile var progress = 0.0
        @Volatile var bytes = 0L
        @Volatile var lastEmit = 0L

        fun json(): JSONObject = JSONObject().put("id", id).put("title", title).put("author", author)
            .put("state", "downloading").put("progress", progress).put("size", bytes)
    }

    /** 用户删掉了正在下载的书 */
    private class Cancelled : Exception()

    /** 超过了下载上限 */
    private class OverLimit(msg: String) : IOException(msg)

    /** 书库明确拒绝了（4xx），重试也没用 */
    private class Refused(msg: String) : IOException(msg)

    /** 排队或正在下载的任务 */
    private val tasks = ConcurrentHashMap<Int, Task>()

    /** 这次打开 App 后下载失败、手机上又没留下文件夹的书（比如一开始就连不上书库），列表里显示“下载失败” */
    private val failed = ConcurrentHashMap<Int, JSONObject>()

    /** 每本书占的空间（算一次记下来；开始下载、删除时清掉） */
    private val sizes = ConcurrentHashMap<Int, Long>()
    private val pool = Executors.newSingleThreadExecutor()
    private val store get() = app.store

    init {
        // 和下载用同一个线程：整理完上次留下的，才开始新的下载
        pool.execute { try { cleanup() } catch (e: Throwable) {} }
    }

    // ---------- 给网页的列表 ----------

    /** 下载到手机的书：下载完的、失败的、正在下载和排队的，每本都带 size（字节）。旧版网页也用这个格式 */
    fun list(): JSONArray {
        val out = JSONArray()
        val running = tasks.values.filter { !it.cancelled }
        val skip = HashSet<Int>()
        running.forEach { skip += it.id }
        skip += failed.keys
        for (d in store.root.listFiles() ?: emptyArray()) {
            val id = d.name.toIntOrNull() ?: continue
            if (id in skip || !d.isDirectory) continue
            diskItem(id, d)?.let { out.put(it) }
        }
        for (j in failed.values) out.put(j)
        for (t in running) out.put(t.json())
        return out
    }

    /** 新版网页用：{"items": 和 list() 一样, "total_size": 一共占多少字节, "limit_mb": 下载上限（0 表示不限）} */
    fun info(): JSONObject {
        val items = list()
        var total = 0L
        for (i in 0 until items.length()) total += items.optJSONObject(i)?.optLong("size") ?: 0L
        return JSONObject().put("items", items).put("total_size", total).put("limit_mb", limitMb())
    }

    private fun diskItem(id: Int, d: File): JSONObject? {
        val st = OfflineStore.readJson(File(d, "state.json")) ?: return null
        return when (st.optString("state")) {
            "done" -> {
                val b = store.book(id) ?: return null
                JSONObject().put("id", id).put("title", b.optString("title")).put("author", b.optString("author"))
                    .put("state", "done").put("size", sizeOf(id, st)).put("format", store.readInfo(id)?.optString("format") ?: "")
            }
            // 没有任务却是 downloading：上次下载到一半 App 被关掉了
            "downloading", "error" -> JSONObject().put("id", id).put("title", st.optString("title")).put("author", st.optString("author"))
                .put("state", "error").put("error", st.optString("error").ifEmpty { "下载中断了" }).put("progress", 0).put("size", sizeOf(id, null))
            else -> null
        }
    }

    /** 一本书占的空间：下载完的记在 state.json 里，没有记的（旧版下载的、没下完的）算一次记下来 */
    private fun sizeOf(id: Int, st: JSONObject?): Long {
        sizes[id]?.let { return it }
        val s = st?.optLong("size")?.takeIf { it > 0 } ?: try { store.size(id) } catch (e: Exception) { 0L }
        sizes[id] = s
        return s
    }

    private fun item(id: Int): JSONObject? {
        tasks[id]?.takeIf { !it.cancelled }?.let { return it.json() }
        failed[id]?.let { return it }
        val d = store.dir(id)
        return if (d.isDirectory) diskItem(id, d) else null
    }

    private fun emit(id: Int) {
        Events.emit("download", item(id) ?: JSONObject().put("id", id).put("state", "removed"))
    }

    // ---------- 开始、删除 ----------

    /** 开始下载（也用来重试）。auto：自动下载在读的书，失败了不弹提示 */
    fun start(id: Int, hint: JSONObject?, auto: Boolean = false) {
        val t: Task
        synchronized(this) {
            val cur = tasks[id]
            if (cur != null && !cur.cancelled) return
            t = Task(id, auto, "dl:$id:${seq.incrementAndGet()}")
            val st = OfflineStore.readJson(File(store.dir(id), "state.json"))
            t.title = hint?.optString("title")?.takeIf { it.isNotEmpty() } ?: st?.optString("title")?.takeIf { it.isNotEmpty() }
                ?: store.book(id)?.optString("title") ?: ""
            t.author = hint?.optString("author")?.takeIf { it.isNotEmpty() } ?: st?.optString("author") ?: ""
            tasks[id] = t
            failed.remove(id)
            sizes.remove(id)
        }
        emit(id)
        TransferService.begin(app, t.job, t.title.ifEmpty { "下载到手机" }, "排队中")
        pool.execute { run(t) }
    }

    /** 删掉手机上的这本书。正在下载的只做标记，由下载线程收尾（直接删目录的话，下载线程再写文件会出错） */
    fun remove(id: Int) {
        val t = synchronized(this) {
            failed.remove(id)
            sizes.remove(id)
            tasks.remove(id)?.also {
                it.cancelled = true
                // 先记下“已删除”：万一 App 在下载线程收尾前被关掉，下次打开时也会清掉
                val f = File(store.dir(id), "state.json")
                if (f.exists()) try { writeJson(f, JSONObject().put("state", "removed")) } catch (e: Exception) {}
            }
        }
        if (t == null) try { store.remove(id) } catch (e: Exception) {}
        emit(id)
    }

    // ---------- 下载 ----------

    /** 线程池里跑的任务：异常跑出去整个 App 会闪退，所以这里什么都接住 */
    private fun run(t: Task) {
        try {
            download(t)
        } catch (e: Throwable) {
            try {
                // 用户删掉了：没下完的文件全部删掉；否则记下失败
                if (t.cancelled) store.remove(t.id) else fail(t, e)
            } catch (x: Throwable) {}
        } finally {
            try { TransferService.end(t.job) } catch (x: Throwable) {}
        }
    }

    private fun download(t: Task) {
        val id = t.id
        check(t)
        val link = app.link
        if (!link.online && link.probe() == null) throw IOException("连不上书库")
        val book = retry(t) { link.get("/api/books/$id") }
        book.remove("files")
        val info = retry(t) { link.get("/api/books/$id/read") }
        val orig = info.getString("url")
        info.put("orig_url", orig)
        t.title = book.optString("title").ifEmpty { t.title }
        t.author = book.optString("author")
        progress(t, 0.0, t.bytes, force = true)

        // 下载前估算大小：超过设置的上限，先删最早下载、已经读完的书腾地方，还不够就不下载
        val room = Room(id)
        room.ensure(estimate(info))

        val dir = store.dir(id)
        val stFile = File(dir, "state.json")
        val st = OfflineStore.readJson(stFile)?.takeIf { it.optString("state") == "downloading" || it.optString("state") == "error" } ?: JSONObject()
        st.put("state", "downloading").put("title", t.title).put("author", t.author).remove("error")
        synchronized(this) {
            check(t)
            if (!dir.isDirectory && !dir.mkdirs()) throw IOException("没法写入手机存储")
            writeJson(stFile, st)
        }

        if (info.optString("reader") == "comic") pages(t, info, dir, room, st) else file(t, info, orig, dir, room, st)
        check(t)

        // 封面、进度、书签：小文件，下不来也不影响阅读
        try {
            val r = Http.request(link.url("/api/books/$id/cover"), token = app.config.token)
            if (r.ok) writeAtomic(File(dir, "cover.jpg"), r.body)
        } catch (e: Exception) {}
        try {
            val p = link.get("/api/books/$id/progress")
            if (p.optLong("updated_at") > 0) writeJson(File(dir, "progress.json"), p)
        } catch (e: Exception) {}
        try {
            writeAtomic(File(dir, "bookmarks.json"), link.getArray("/api/books/$id/bookmarks").toString().toByteArray(Charsets.UTF_8))
        } catch (e: Exception) {}
        writeJson(File(dir, "book.json"), book)
        writeJson(File(dir, "read.json"), info)

        synchronized(this) {
            check(t)
            val size = try { store.size(id) } catch (e: Exception) { 0L }
            writeJson(stFile, JSONObject().put("state", "done").put("at", System.currentTimeMillis()).put("size", size)
                .put("title", t.title).put("author", t.author).put("read_state", book.optString("read_state")))
            sizes[id] = size
            tasks.remove(id, t)
        }
        emit(id)
    }

    /** 漫画按页下载（CBR、7z 也一样），离线时由 App 按页提供。重试时已经下好的页跳过，一页出错重试几次再算失败 */
    private fun pages(t: Task, info: JSONObject, dir: File, room: Room, st: JSONObject) {
        val link = app.link
        val token = app.config.token
        val fid = info.getInt("file_id")
        val pages = retry(t) { link.get("/api/files/$fid/pages") }
        val n = pages.getInt("count")
        val pdir = File(dir, "pages")
        // 上次下的是别的文件（书库上换了文件、页数变了）：已经下好的页不能用
        val version = "$fid:$n"
        if (st.optString("pages") != version) {
            pdir.deleteRecursively()
            st.put("pages", version)
            synchronized(this) {
                check(t)
                writeJson(File(dir, "state.json"), st)
            }
        }
        if (!pdir.isDirectory && !pdir.mkdirs()) throw IOException("没法写入手机存储")
        // 已经下好的页：下载中的页先存成 .part，下完才改名，所以有正式名字、不是空的都是完整的
        val have = HashSet<String>()
        var bytes = 0L
        for (f in pdir.listFiles() ?: emptyArray()) {
            if (f.name.endsWith(".part") || f.length() == 0L) f.delete()
            else { have += f.nameWithoutExtension; bytes += f.length() }
        }
        for (i in 0 until n) {
            check(t)
            val key = i.toString().padStart(4, '0')
            if (key !in have) {
                val r = retry(t) {
                    val r = Http.request(link.url("/api/files/$fid/pages/$i"), token = token, timeoutMs = 30000)
                    if (!r.ok) throw if (r.code in 400..499 && r.code != 408 && r.code != 429) Refused(r.errorMessage()) else IOException(r.errorMessage())
                    r
                }
                val ext = when (r.header("Content-Type")?.substringBefore(';')?.trim()) {
                    "image/png" -> "png"
                    "image/webp" -> "webp"
                    "image/gif" -> "gif"
                    "image/avif" -> "avif"
                    else -> "jpg"
                }
                writeAtomic(File(pdir, "$key.$ext"), r.body)
                bytes += r.body.size
                room.ensure(bytes)
            }
            progress(t, (i + 1).toDouble() / n, bytes)
        }
        writeJson(File(dir, "pages.json"), pages)
    }

    /** 一个文件的书（EPUB、PDF、转换好的 TXT……）。重试时上次已经下好、还是书库现在这个版本的就不再下 */
    private fun file(t: Task, info: JSONObject, orig: String, dir: File, room: Room, st: JSONObject) {
        val ext = info.optString("name").substringAfterLast('.', "").ifEmpty { info.optString("format") }.lowercase().ifEmpty { "bin" }
        val dest = File(dir, "book.$ext")
        // 地址一样就是同一个版本（TXT 的地址里带着分章规则的版本号）
        if (dest.isFile && dest.length() > 0 && st.optString("file") == orig) {
            t.bytes = dest.length()
            return
        }
        for (f in dir.listFiles() ?: emptyArray()) if (OfflineStore.isBookFile(f) || f.name.endsWith(".part")) f.delete()
        var checked = false
        retry(t, 2) {
            Http.download(app.link.url(orig), app.config.token, dest) { done, total ->
                check(t)
                // 知道真正的大小以后再核对一次上限
                if (!checked && total > 0) {
                    checked = true
                    room.ensure(total)
                }
                progress(t, if (total > 0) done.toDouble() / total else -1.0, done)
            }
        }
        st.put("file", orig)
        synchronized(this) {
            check(t)
            writeJson(File(dir, "state.json"), st)
        }
    }

    /** 下载失败：记下原因（列表里显示“下载失败”，可以重试），删掉没下完的临时文件 */
    private fun fail(t: Task, e: Throwable) {
        val id = t.id
        val msg = when (e) {
            is OutOfMemoryError -> "手机内存不够"
            is SocketTimeoutException -> "网络超时"
            is UnknownHostException, is ConnectException -> "连不上书库"
            else -> e.message?.takeIf { it.isNotBlank() } ?: e.javaClass.simpleName
        }
        var kept = false
        try {
            val d = store.dir(id)
            for (f in d.listFiles() ?: emptyArray()) if (f.name.endsWith(".part") || f.name.endsWith(".tmp")) f.delete()
            for (f in File(d, "pages").listFiles() ?: emptyArray()) if (f.name.endsWith(".part")) f.delete()
            synchronized(this) {
                val f = File(d, "state.json")
                if (!t.cancelled && f.exists()) {
                    val st = OfflineStore.readJson(f) ?: JSONObject()
                    writeJson(f, st.put("state", "error").put("error", msg).put("title", t.title).put("author", t.author))
                    kept = true
                }
            }
        } catch (x: Throwable) {}
        val cancelled = synchronized(this) {
            if (t.cancelled) true else {
                tasks.remove(id, t)
                sizes.remove(id)
                // 手机上没留下文件夹（一开始就失败了）：记在内存里，这次打开 App 期间列表里能看到
                if (!kept && !t.auto) failed[id] = JSONObject().put("id", id).put("title", t.title).put("author", t.author)
                    .put("state", "error").put("error", msg).put("progress", 0).put("size", 0)
                false
            }
        }
        if (cancelled) {
            try { store.remove(id) } catch (x: Throwable) {}
            return
        }
        emit(id)
        if (!t.auto) toast(if (e is OverLimit) msg else "《${t.title}》下载失败：$msg")
    }

    private fun progress(t: Task, fraction: Double, bytes: Long, force: Boolean = false) {
        t.progress = fraction.coerceIn(0.0, 1.0)
        t.bytes = bytes
        val now = SystemClock.uptimeMillis()
        if (!force && now - t.lastEmit < 300) return
        t.lastEmit = now
        emit(t.id)
        val pct = if (fraction >= 0) "${(fraction * 100).toInt()}% · " else ""
        TransferService.update(t.job, "下载到手机 · $pct${TransferService.mb(bytes)}", fraction, t.title)
    }

    private fun check(t: Task) {
        if (t.cancelled) throw Cancelled()
    }

    /** 出错重试几次（网络断了一下、书库忙）；书库明确拒绝的、删掉了的、超过上限的不重试 */
    private fun <T> retry(t: Task, times: Int = 3, block: () -> T): T {
        var wait = 1000L
        var n = 0
        while (true) {
            check(t)
            try {
                return block()
            } catch (e: Exception) {
                if (e is Cancelled || e is OverLimit || e is Refused || t.cancelled || ++n >= times) throw e
            }
            var left = wait
            while (left > 0) {
                check(t)
                Thread.sleep(minOf(left, 200L))
                left -= 200
            }
            wait *= 2
        }
    }

    /** 下载前估算大小：书库上这个文件的大小（TXT 转成的 EPUB 一般比原文件小；漫画按页下载和压缩包差不多大） */
    private fun estimate(info: JSONObject): Long {
        val fid = info.optLong("file_id")
        val files = info.optJSONArray("files") ?: return 0
        for (i in 0 until files.length()) {
            val f = files.optJSONObject(i) ?: continue
            if (f.optLong("id") == fid) return f.optLong("size")
        }
        return 0
    }

    // ---------- 下载上限、读完清理 ----------

    /** 设置里的下载上限（MB），0 表示不限 */
    fun limitMb(): Int = try { app.config.prefs.getInt("download_limit_mb", 0).coerceAtLeast(0) } catch (e: Exception) { 0 }

    /**
     * 一本书下载时核对上限：这本书一共要 need 字节，加上手机上别的书超过上限时，先删最早下载、已经读完的书；
     * 还不够就不下载（OverLimit）
     */
    private inner class Room(private val id: Int) {
        private val limit = limitMb() * 1048576L
        private var others = -1L

        fun ensure(need: Long) {
            if (limit <= 0) return
            if (others < 0) others = usedExcept(id)
            if (others + need <= limit) return
            others = usedExcept(id)
            if (others + need > limit) {
                freeFinished(id, others + need - limit)
                others = usedExcept(id)
            }
            if (others + need > limit) throw OverLimit("手机上的书已经超过 ${limit / 1048576} MB 的上限，在设置里调大或删掉一些")
        }
    }

    /** 除了这本书，手机上的书一共占多少字节 */
    private fun usedExcept(id: Int): Long {
        var sum = 0L
        for (d in store.root.listFiles() ?: emptyArray()) {
            val bid = d.name.toIntOrNull() ?: continue
            if (bid == id || !d.isDirectory) continue
            val st = OfflineStore.readJson(File(d, "state.json"))
            if (st?.optString("state") == "removed") continue
            sum += sizeOf(bid, st?.takeIf { it.optString("state") == "done" })
        }
        return sum
    }

    /** 删最早下载、下载以后读完了的书，直到腾出 need 字节 */
    private fun freeFinished(id: Int, need: Long) {
        val cands = store.ids().filter { it != id && !tasks.containsKey(it) }
            .mapNotNull { bid -> OfflineStore.readJson(File(store.dir(bid), "state.json"))?.let { bid to it } }
            .sortedBy { it.second.optLong("at") }
        if (cands.isEmpty()) return
        val server = states(cands.map { it.first })
        var freed = 0L
        for ((bid, st) in cands) {
            if (freed >= need) break
            if (!finished(bid, st, server)) continue
            freed += sizeOf(bid, st)
            try { store.remove(bid) } catch (e: Exception) {}
            sizes.remove(bid)
            emit(bid)
        }
    }

    /**
     * 设置里打开了“读完自动清理”：App 启动连上书库时，查一下下载过的书在书库上的阅读状态，
     * 下载以后读完了的就删掉手机上的副本（读完以后又特意下载来重读的不删）
     */
    fun autoClean() {
        if (!app.config.bool("auto_clean_finished", false) || !app.link.online) return
        val cands = store.ids().filter { !tasks.containsKey(it) }
        if (cands.isEmpty()) return
        val server = states(cands) ?: return // 查不到书库上的状态就先不删
        for (id in cands) {
            val st = OfflineStore.readJson(File(store.dir(id), "state.json")) ?: continue
            if (!finished(id, st, server) || tasks.containsKey(id)) continue
            try { store.remove(id) } catch (e: Exception) {}
            sizes.remove(id)
            emit(id)
        }
    }

    /**
     * 这本书是不是下载以后读完的。server 是书库上的阅读状态（finished_at 是秒，state.json 的 at 是毫秒）；
     * 查不到书库时（server 为 null）看手机上记的：下载时还没读完、后来在手机上读完了
     */
    private fun finished(id: Int, st: JSONObject, server: JSONObject?): Boolean {
        if (server == null) return store.book(id)?.optString("read_state") == "done" && st.optString("read_state") != "done"
        val s = server.optJSONObject(id.toString()) ?: return false
        if (s.optString("read_state") != "done") return false
        val fin = s.optLong("finished_at") * 1000
        val at = st.optLong("at")
        return fin <= 0 || at <= 0 || fin >= at
    }

    /** 书库上这些书的阅读状态 {"12":{"read_state":"done","finished_at":…},…}；连不上、旧版书库没有这个接口时返回 null */
    private fun states(ids: List<Int>): JSONObject? {
        if (ids.isEmpty() || !app.link.online) return null
        return try {
            val out = JSONObject()
            for (part in ids.chunked(200)) {
                val j = app.link.get("/api/books/states?ids=" + part.joinToString(","), 10000)
                for (k in j.keys()) out.put(k, j.opt(k))
            }
            out
        } catch (e: Exception) {
            null
        }
    }

    /** 连着内网时，把“在读”的书自动下载到手机（漫画太大，不自动下载） */
    fun autoDownload() {
        if (!app.config.bool("auto_download", false) || app.link.route != "lan") return
        try {
            val items = app.link.get("/api/books?reading=1&limit=10&sort=recent").getJSONArray("items")
            for (i in 0 until items.length()) {
                val b = items.getJSONObject(i)
                val id = b.getInt("id")
                if (b.optString("kind") != "comic" && !store.isDone(id) && !tasks.containsKey(id)) start(id, b, auto = true)
            }
        } catch (e: Exception) {}
    }

    // ---------- 整理、写盘 ----------

    /**
     * 打开 App 时整理上次留下的：下载到一半被关掉的标成“下载中断了”（列表里能看到，可以接着下）；
     * 下载中被删掉的、看不出是哪本书的（旧版留下的）、删到一半的文件夹，以及临时文件都清掉
     */
    private fun cleanup() {
        for (d in store.root.listFiles() ?: return) {
            try {
                if (d.name.startsWith(OfflineStore.TRASH)) {
                    d.deleteRecursively()
                    continue
                }
                val id = d.name.toIntOrNull() ?: continue
                if (!d.isDirectory || tasks.containsKey(id)) continue
                val f = File(d, "state.json")
                val st = OfflineStore.readJson(f)
                when (st?.optString("state")) {
                    "done" -> {}
                    "downloading", "error" -> if (st.optString("title").isEmpty()) d.deleteRecursively() else {
                        d.walkTopDown().filter { it.isFile && (it.name.endsWith(".part") || it.name.endsWith(".tmp")) }.forEach { it.delete() }
                        if (st.optString("state") == "downloading") writeJson(f, st.put("state", "error").put("error", "下载中断了"))
                    }
                    else -> d.deleteRecursively()
                }
            } catch (e: Exception) {}
        }
    }

    /** 写 JSON：先写临时文件再改名，写到一半闪退、断电也不会留下坏文件。出错抛 IOException，由调用的地方处理 */
    private fun writeJson(f: File, j: JSONObject) {
        val tmp = File(f.parentFile, "." + f.name + ".tmp")
        tmp.writeText(j.toString())
        if (!tmp.renameTo(f)) {
            tmp.delete()
            f.writeText(j.toString())
        }
    }

    /** 写文件：先写 .part，写完才改成正式的名字 */
    private fun writeAtomic(f: File, data: ByteArray) {
        val tmp = File(f.parentFile, f.name + ".part")
        tmp.writeBytes(data)
        if (!tmp.renameTo(f)) {
            tmp.delete()
            throw IOException("没法写入手机存储")
        }
    }

    private fun toast(msg: String) = app.main.post { Toast.makeText(app, msg, Toast.LENGTH_LONG).show() }

    companion object {
        private val seq = AtomicInteger()
    }
}
