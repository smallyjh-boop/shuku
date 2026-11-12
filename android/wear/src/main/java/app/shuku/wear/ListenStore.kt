package app.shuku.wear

import android.content.Context
import java.io.File
import java.io.IOException
import java.security.MessageDigest
import java.util.concurrent.atomic.AtomicLong
import kotlin.math.roundToInt

/**
 * 听书合成好的语音，也就是手表上的“离线听书包”：filesDir/listen/<书>/<文件>_<章>_<句>_<文字摘要>_<语速>.mp3。
 * 每一句一个文件（文字相同的两句也各是各的，删的时候只删自己的），听过的、换书时删掉，总大小有上限。
 * 放在 filesDir 而不是缓存目录：手表空间紧张时系统清缓存，不会把准备好的离线听书删掉。
 * 这里的方法都会读写文件，在后台线程调用。
 */
object ListenStore {
    /** 离线听书包最多占多少空间（书库的微软语音 48kbps，60MB 大约能放两个半小时） */
    const val MAX_BYTES = 60L * 1024 * 1024

    private val NAME = Regex("""(\d+)_(\d+)_(\d+)_([0-9a-f]+)_(\d+)\.mp3""")
    private val used = AtomicLong(-1) // 已经占了多少；-1 表示还没统计

    fun root(ctx: Context) = File(ctx.filesDir, "listen")
    private fun dir(ctx: Context, bookId: Long) = File(root(ctx), bookId.toString())

    private fun speedKey(speed: Double) = (speed * 100).roundToInt()

    private fun digest(text: String): String =
        MessageDigest.getInstance("SHA-1").digest(text.toByteArray()).take(4).joinToString("") { "%02x".format(it) }

    /** 一句的语音文件：书、文件、第几章、这一章的第几句、语速都在名字里，文字改了（摘要对不上）就不会用错 */
    fun file(ctx: Context, bookId: Long, fileId: Long, chap: Int, n: Int, speed: Double, text: String) =
        File(dir(ctx, bookId), "${fileId}_${chap}_${n}_${digest(text)}_${speedKey(speed)}.mp3")

    /** 同一句用别的语速合成好的（没网时改了语速：先接着用准备好的，不至于没得听） */
    fun otherSpeeds(ctx: Context, bookId: Long, fileId: Long, chap: Int, n: Int, speed: Double, text: String): List<File> =
        ListenService.SPEEDS.filter { speedKey(it) != speedKey(speed) }.map { file(ctx, bookId, fileId, chap, n, it, text) }

    /** 这个文件是按什么语速合成的（名字里写着）；书库听书包的都是 1 倍速，放的时候手表自己调语速 */
    fun speedOf(f: File): Double = NAME.matchEntire(f.name)?.groupValues?.get(5)?.toIntOrNull()?.let { it / 100.0 } ?: 1.0

    /** 现在占了多少空间 */
    fun bytes(ctx: Context): Long {
        val u = used.get()
        return if (u >= 0) u else rescan(ctx)
    }

    @Synchronized
    private fun rescan(ctx: Context): Long {
        val n = root(ctx).walkTopDown().filter { it.isFile }.sumOf { it.length() }
        used.set(n)
        return n
    }

    private fun adjust(delta: Long) {
        if (used.get() >= 0) used.addAndGet(delta)
    }

    /** 存一句的语音：先写临时文件再改名，不会留下半截的文件；同一句别的语速的文件用不着了，一起删掉 */
    fun write(ctx: Context, f: File, bytes: ByteArray, others: List<File>) {
        val d = f.parentFile ?: throw IOException("存不下语音")
        d.mkdirs()
        val tmp = File.createTempFile("tts", ".part", d)
        try {
            tmp.writeBytes(bytes)
            val old = f.length()
            if (!tmp.renameTo(f)) throw IOException("存不下语音")
            adjust(bytes.size - old)
        } finally {
            if (tmp.exists()) tmp.delete()
        }
        others.forEach { delete(it) }
    }

    fun delete(f: File) {
        val n = f.length()
        if (f.delete()) adjust(-n)
    }

    /**
     * 开始听一本书：别的书的语音删掉（换书了）；这本书里换了格式的（文件对不上）、fromChap 章以前的（听过了）、
     * 很早以前没写完的临时文件，也删掉。
     */
    fun prepare(ctx: Context, bookId: Long, fileId: Long, fromChap: Int) {
        root(ctx).listFiles()?.forEach { if (it.name != bookId.toString()) it.deleteRecursively() }
        val old = System.currentTimeMillis() - 5 * 60_000L
        dir(ctx, bookId).listFiles()?.forEach { f ->
            val m = NAME.matchEntire(f.name)
            when {
                m == null -> if (f.lastModified() < old) f.delete()
                m.groupValues[1].toLong() != fileId || m.groupValues[2].toInt() < fromChap -> f.delete()
            }
        }
        rescan(ctx)
    }

    /** 听到第 chap 章了：再往前一章之前的都删掉（上一句、上一章开头还能接着用） */
    fun dropBefore(ctx: Context, bookId: Long, fileId: Long, chap: Int) {
        var freed = 0L
        dir(ctx, bookId).listFiles()?.forEach { f ->
            val m = NAME.matchEntire(f.name) ?: return@forEach
            if (m.groupValues[1].toLong() != fileId || m.groupValues[2].toInt() < chap - 1) {
                val n = f.length()
                if (f.delete()) freed += n
            }
        }
        if (freed > 0) adjust(-freed)
    }

    /** 这本书听完了 */
    fun clearBook(ctx: Context, bookId: Long) {
        dir(ctx, bookId).deleteRecursively()
        rescan(ctx)
    }

    /** 断开书库时全部删掉 */
    fun clearAll(ctx: Context) {
        root(ctx).deleteRecursively()
        used.set(0)
    }

    /** 很久没听了（一周没动过）：删掉，别一直占着手表的空间 */
    fun expire(ctx: Context, maxAgeMs: Long) {
        val cut = System.currentTimeMillis() - maxAgeMs
        root(ctx).listFiles()?.forEach { d ->
            val newest = d.walkTopDown().filter { it.isFile }.maxOfOrNull { it.lastModified() } ?: 0L
            if (newest < cut) d.deleteRecursively()
        }
        rescan(ctx)
    }
}
