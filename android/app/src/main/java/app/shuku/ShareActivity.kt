package app.shuku

import android.app.Activity
import android.app.AlertDialog
import android.app.Notification
import android.app.NotificationManager
import android.content.Intent
import android.content.res.Configuration
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import android.widget.Toast
import app.shuku.core.Http
import org.json.JSONObject
import java.io.IOException
import java.io.InputStream
import java.net.URLEncoder
import java.util.concurrent.Executors

/**
 * 从别的 App 分享或“用书库打开”电子书文件：上传到书库的投递目录，自动识别入库。
 * 分享的是网址（比如浏览器里的下载链接）：让 NAS 自己去下载。
 *
 * 读文件放在后台线程：云盘里的文件要先下载，在主线程打开会卡住（ANR）。读取权限跟着这个界面，
 * 所以文件打开以前不能关掉它（超过半秒还没打开就显示“正在读取”，可以取消）；打开以后交给前台服务里的上传线程。
 */
class ShareActivity : Activity() {
    private val app get() = ShukuApp.app
    @Volatile private var cancelled = false
    private var handedOff = false
    private var job = ""
    private var waiting: AlertDialog? = null
    private val showWaiting = Runnable { showWaiting() }

    private class Upload(val name: String, val size: Long, val stream: InputStream)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (!app.config.configured) {
            Toast.makeText(this, "请先打开书库 App 连接书库", Toast.LENGTH_LONG).show()
            startActivity(Intent(this, MainActivity::class.java))
            finish()
            return
        }
        val uris = ArrayList<Uri>()
        when (intent.action) {
            Intent.ACTION_SEND -> streamExtra()?.let { uris += it }
            Intent.ACTION_SEND_MULTIPLE -> uris += streamListExtra()
            Intent.ACTION_VIEW -> intent.data?.let { uris += it }
        }
        val text = intent.getStringExtra(Intent.EXTRA_TEXT)?.trim()
        if (uris.isEmpty() && text != null) {
            val url = Regex("https?://\\S+").find(text)?.value
            if (url != null) fetchLink(url) else Toast.makeText(this, "没有找到电子书文件或下载链接", Toast.LENGTH_LONG).show()
            finish()
            return
        }
        if (uris.isEmpty()) {
            finish()
            return
        }
        Toast.makeText(this, "正在把 ${uris.size} 个文件加入书库…", Toast.LENGTH_SHORT).show()
        // 现在界面在前台，系统允许开前台服务：读文件、上传都在它的保护下，切到别的 App 也不会停
        job = "up:${System.nanoTime()}"
        TransferService.begin(this, job, "加入书库", "正在读取 ${uris.size} 个文件…", upload = true)
        app.main.postDelayed(showWaiting, 500)
        val resolver = contentResolver
        app.io.execute {
            val items = ArrayList<Upload>()
            try {
                for (u in uris) {
                    if (cancelled) break
                    try {
                        val name = displayName(u)
                        val size = sizeOf(u)
                        val s = resolver.openInputStream(u) ?: continue
                        items += Upload(name, size, s)
                    } catch (e: Exception) {
                        // 读不到的文件跳过（权限、云盘下载失败等）
                    }
                }
            } finally {
                app.main.post { opened(items) }
            }
        }
    }

    /** 文件都打开了（主线程）：交给上传线程，关掉这个界面 */
    private fun opened(items: List<Upload>) {
        app.main.removeCallbacks(showWaiting)
        try { waiting?.dismiss() } catch (e: Exception) {}
        waiting = null
        if (cancelled) {
            items.forEach { try { it.stream.close() } catch (e: Exception) {} }
            TransferService.end(job)
            return
        }
        handedOff = true
        if (items.isEmpty()) {
            Toast.makeText(app, "读不到这个文件", Toast.LENGTH_LONG).show()
            TransferService.end(job)
        } else upload(job, items)
        finish()
    }

    private fun showWaiting() {
        if (isFinishing || isDestroyed || handedOff) return
        val night = (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES
        waiting = AlertDialog.Builder(this, if (night) android.R.style.Theme_Material_Dialog_Alert else android.R.style.Theme_Material_Light_Dialog_Alert)
            .setTitle("加入书库")
            .setMessage("正在读取文件…（云盘里的文件要先下载）")
            .setNegativeButton("取消") { _, _ -> cancel() }
            .setOnCancelListener { cancel() }
            .show()
    }

    private fun cancel() {
        cancelled = true
        finish()
    }

    override fun onDestroy() {
        app.main.removeCallbacks(showWaiting)
        try { waiting?.dismiss() } catch (e: Exception) {}
        waiting = null
        // 还没交出去就被关掉了（按了返回键）：读文件的线程看到后会关掉已经打开的文件
        if (!handedOff) cancelled = true
        super.onDestroy()
    }

    private fun fetchLink(url: String) {
        app.io.execute {
            val msg = try {
                if (!app.link.online) app.link.probe()
                val r = app.link.send("POST", "/api/fetch", JSONObject().put("url", url).put("source", "手机分享"))
                if (r.ok) "书库开始下载这个链接，完成后自动入库" else r.errorMessage()
            } catch (e: Exception) {
                "连不上书库：${e.message}"
            }
            app.main.post { Toast.makeText(app, msg, Toast.LENGTH_LONG).show() }
        }
    }

    @Suppress("DEPRECATION")
    private fun streamExtra(): Uri? =
        if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java) else intent.getParcelableExtra(Intent.EXTRA_STREAM)

    @Suppress("DEPRECATION")
    private fun streamListExtra(): List<Uri> =
        (if (Build.VERSION.SDK_INT >= 33) intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java) else intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM)) ?: emptyList()

    private fun displayName(uri: Uri): String {
        try {
            contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
                if (c.moveToFirst()) c.getString(0)?.takeIf { it.isNotBlank() }?.let { return it }
            }
        } catch (e: Exception) {}
        return uri.lastPathSegment?.substringAfterLast('/') ?: "book"
    }

    private fun sizeOf(uri: Uri): Long {
        try {
            contentResolver.query(uri, arrayOf(OpenableColumns.SIZE), null, null, null)?.use { c ->
                if (c.moveToFirst() && !c.isNull(0)) return c.getLong(0)
            }
        } catch (e: Exception) {}
        return -1
    }

    companion object {
        private const val NOTIF_ID = 31

        /** 一次传一个文件（几次分享也排着队传） */
        private val pool = Executors.newSingleThreadExecutor()

        private fun upload(job: String, items: List<Upload>) = pool.execute {
            val app = ShukuApp.app
            var ok = 0
            var lastErr = ""
            for ((i, item) in items.withIndex()) {
                val label = if (items.size > 1) "（${i + 1}/${items.size}）" else ""
                try {
                    TransferService.update(job, "$label${item.name}", -1.0)
                    val base = app.link.base ?: app.link.probe() ?: throw IOException("连不上书库")
                    var lastPct = -1
                    val r = item.stream.use { input ->
                        Http.upload("$base/api/upload?path=" + URLEncoder.encode(item.name, "UTF-8"), app.config.token, input, item.size) { done ->
                            val pct = if (item.size > 0) (done * 100 / item.size).toInt() else -1
                            if (pct != lastPct) {
                                lastPct = pct
                                TransferService.update(job, "$label${item.name}", if (pct >= 0) pct / 100.0 else -1.0)
                            }
                        }
                    }
                    if (r.ok) ok++ else lastErr = r.errorMessage()
                } catch (e: Throwable) {
                    lastErr = e.message ?: "上传失败"
                    try { item.stream.close() } catch (x: Exception) {}
                }
            }
            try {
                val msg = if (ok == items.size) "已加入书库，正在识别整理" else "成功 $ok 个，失败 ${items.size - ok} 个：$lastErr"
                app.getSystemService(NotificationManager::class.java)?.notify(NOTIF_ID + 1, Notification.Builder(app, ShukuApp.CH_TASK)
                    .setSmallIcon(android.R.drawable.stat_sys_upload_done)
                    .setContentTitle("书库").setContentText(msg).setAutoCancel(true).build())
                app.main.post { Toast.makeText(app, msg, Toast.LENGTH_LONG).show() }
            } catch (e: Throwable) {
            } finally {
                TransferService.end(job)
            }
        }
    }
}
