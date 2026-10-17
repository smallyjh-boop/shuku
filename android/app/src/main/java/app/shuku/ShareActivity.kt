package app.shuku

import android.app.Activity
import android.app.Notification
import android.app.NotificationManager
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import android.widget.Toast
import app.shuku.core.Http
import org.json.JSONObject
import java.net.URLEncoder

/**
 * 从别的 App 分享或“用书库打开”电子书文件：上传到书库的投递目录，自动识别入库。
 * 分享的是网址（比如浏览器里的下载链接）：让 NAS 自己去下载。
 */
class ShareActivity : Activity() {
    private val app get() = ShukuApp.app

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
        // 先打开文件，Activity 结束后就没有读取权限了
        val items = uris.mapNotNull { u ->
            try { Triple(displayName(u), sizeOf(u), contentResolver.openInputStream(u) ?: return@mapNotNull null) } catch (e: Exception) { null }
        }
        if (items.isEmpty()) {
            Toast.makeText(this, "读不到这个文件", Toast.LENGTH_LONG).show()
            finish()
            return
        }
        app.io.execute {
            val nm = getSystemService(NotificationManager::class.java)!!
            var ok = 0
            var lastErr = ""
            for ((i, item) in items.withIndex()) {
                val (name, size, stream) = item
                val notify = { pct: Int ->
                    nm.notify(NOTIF_ID, Notification.Builder(this, ShukuApp.CH_TASK).setSmallIcon(android.R.drawable.stat_sys_upload)
                        .setContentTitle("加入书库（${i + 1}/${items.size}）").setContentText(name).setProgress(100, pct, pct < 0).setOngoing(true).build())
                }
                try {
                    val base = app.link.base ?: app.link.probe() ?: throw Exception("连不上书库")
                    notify(-1)
                    var lastPct = -1
                    val r = stream.use { input ->
                        Http.upload("$base/api/upload?path=" + URLEncoder.encode(name, "UTF-8"), app.config.token, input, size) { done ->
                            val pct = if (size > 0) (done * 100 / size).toInt() else -1
                            if (pct != lastPct && pct % 5 == 0) { lastPct = pct; notify(pct) }
                        }
                    }
                    if (r.ok) ok++ else lastErr = r.errorMessage()
                } catch (e: Exception) {
                    lastErr = e.message ?: "上传失败"
                    try { stream.close() } catch (x: Exception) {}
                }
            }
            nm.cancel(NOTIF_ID)
            val msg = if (ok == items.size) "已加入书库，正在识别整理" else "成功 $ok 个，失败 ${items.size - ok} 个：$lastErr"
            nm.notify(NOTIF_ID + 1, Notification.Builder(this, ShukuApp.CH_TASK).setSmallIcon(android.R.drawable.stat_sys_upload_done)
                .setContentTitle("书库").setContentText(msg).setAutoCancel(true).build())
            app.main.post { Toast.makeText(app, msg, Toast.LENGTH_LONG).show() }
        }
        finish()
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
    }
}
