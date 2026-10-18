package app.shuku

import android.app.Activity
import android.app.AlertDialog
import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.provider.MediaStore
import android.provider.Settings
import android.widget.Toast
import app.shuku.core.Http
import java.io.File
import java.io.IOException
import java.security.MessageDigest

/**
 * 手机 App、手表 App 的更新。新安装包跟着书库服务器一起发布：服务器升级后，
 * /api/clients 里就是新版本号，/dl/… 能下载安装包，不用登录 GitHub。
 */
object Updater {
    private const val CHECK_EVERY = 3600 * 1000L
    private const val SKIP = "update_skip"
    private const val NOTIF_INSTALL = 41
    private val app get() = ShukuApp.app
    @Volatile private var busy = false

    /** 要用户确认安装、但通知发不出来时，等回到 App 再弹 */
    @Volatile private var pendingConfirm: Intent? = null

    class Info(val versionCode: Int, val versionName: String, val url: String, val sha256: String)

    /** 书库上最新的安装包（kind：android 或 wear），书库里没有时返回 null */
    fun latest(kind: String): Info? {
        val j = Http.getJson(app.link.url("/api/clients"), null, 8000).optJSONObject(kind) ?: return null
        if (j.optString("url").isEmpty()) return null
        return Info(j.optInt("versionCode"), j.optString("versionName"), j.optString("url"), j.optString("sha256"))
    }

    /**
     * 打开 App 时检查（最多 1 小时一次），有新版本就问要不要更新。启动时会有两处同时调用，加锁免得弹两次。
     * 用户对这个版本点过“以后”就不再问（设置里手动“检查更新”照样能更新）
     */
    @Synchronized
    fun checkQuietly(activity: MainActivity) {
        val prefs = app.config.prefs
        val now = System.currentTimeMillis()
        if (busy || now - prefs.getLong("update_checked", 0) < CHECK_EVERY) return
        val info = try { latest("android") } catch (e: Exception) { null } ?: return
        prefs.edit().putLong("update_checked", now).apply()
        val skipped = try { prefs.getInt(SKIP, 0) } catch (e: Exception) { 0 }
        if (info.versionCode > BuildConfig.VERSION_CODE && info.versionCode != skipped) activity.runOnUiThread { ask(activity, info) }
    }

    /** 网页“设置 → 版本与升级”里点“更新” */
    fun checkNow(activity: MainActivity) = app.io.execute {
        val info = try {
            latest("android")
        } catch (e: Exception) {
            activity.runOnUiThread { activity.toast("检查更新失败：${e.message ?: "连不上书库"}") }
            return@execute
        }
        activity.runOnUiThread {
            when {
                info == null -> activity.toast("书库里没有手机 App 的安装包")
                info.versionCode <= BuildConfig.VERSION_CODE -> activity.toast("已经是最新版本（${BuildConfig.VERSION_NAME}）")
                else -> ask(activity, info)
            }
        }
    }

    private fun ask(activity: MainActivity, info: Info) {
        if (activity.isFinishing || activity.isDestroyed) return
        AlertDialog.Builder(activity)
            .setTitle("书库 App 有新版本")
            .setMessage("新版本 ${info.versionName}，现在用的是 ${BuildConfig.VERSION_NAME}。下载后按系统提示安装，下载到手机的书和设置都会保留。")
            .setPositiveButton("更新") { _, _ -> install(activity, info) }
            // 记住这个版本：打开 App 时不再为它提示，想更新时在“设置 → 版本与升级”里点“更新”
            .setNegativeButton("以后") { _, _ -> app.config.prefs.edit().putInt(SKIP, info.versionCode).apply() }
            .show()
    }

    private fun install(activity: MainActivity, info: Info) {
        // 第一次要允许“书库”安装应用
        if (!activity.packageManager.canRequestPackageInstalls()) {
            activity.toast("请打开“允许安装应用”，然后回到书库再点一次“更新”")
            try {
                activity.startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${activity.packageName}")))
            } catch (e: Exception) {}
            return
        }
        if (busy) return
        busy = true
        activity.toast("正在下载新版本……")
        // 下载时开着前台服务：切到别的 App 也不会停
        val job = "update:${info.versionCode}:${System.nanoTime()}"
        TransferService.begin(activity, job, "书库 App ${info.versionName}", "正在下载新版本")
        app.io.execute {
            try {
                val f = fetch(info, "shuku-android.apk") { done, total ->
                    TransferService.update(job, "正在下载新版本 · ${TransferService.mb(done)}", if (total > 0) done.toDouble() / total else -1.0)
                }
                installApk(activity.applicationContext, f)
            } catch (e: Exception) {
                app.main.post { Toast.makeText(app, "更新失败：${e.message ?: e.javaClass.simpleName}", Toast.LENGTH_LONG).show() }
            } finally {
                busy = false
                TransferService.end(job)
            }
        }
    }

    /** 下载安装包到缓存目录，核对校验值 */
    private fun fetch(info: Info, name: String, onProgress: (Long, Long) -> Unit = { _, _ -> }): File {
        val f = File(File(app.cacheDir, "update").apply { mkdirs() }, name)
        Http.download(app.link.url(info.url), null, f, onProgress)
        if (info.sha256.isNotEmpty()) {
            val md = MessageDigest.getInstance("SHA-256")
            f.inputStream().use { input ->
                val buf = ByteArray(64 * 1024)
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    md.update(buf, 0, n)
                }
            }
            val got = md.digest().joinToString("") { "%02x".format(it) }
            if (!got.equals(info.sha256, ignoreCase = true)) {
                f.delete()
                throw IOException("安装包下载不完整，请再试一次")
            }
        }
        return f
    }

    /** 用系统的安装器安装（会弹出系统的确认界面） */
    private fun installApk(ctx: Context, f: File) {
        val installer = ctx.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setAppPackageName(ctx.packageName)
        val id = installer.createSession(params)
        installer.openSession(id).use { s ->
            s.openWrite("shuku.apk", 0, f.length()).use { out ->
                f.inputStream().use { it.copyTo(out) }
                s.fsync(out)
            }
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or (if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0)
            val pi = PendingIntent.getBroadcast(ctx, id, Intent(ctx, InstallReceiver::class.java), flags)
            s.commit(pi.intentSender)
        }
    }

    /**
     * 手表 App：手表不能自己装，下载好安装包放进手机的“下载”文件夹，再交给 Wear OS 工具箱之类的 App 装到手表上
     */
    fun updateWatch(activity: MainActivity) {
        if (busy) return
        busy = true
        activity.toast("正在下载手表 App……")
        app.io.execute {
            try {
                val info = latest("wear") ?: throw IOException("书库里没有手表 App 的安装包")
                if (Build.VERSION.SDK_INT < 29) {
                    // 老版本安卓：交给浏览器下载到“下载”文件夹
                    val url = app.link.url(info.url)
                    activity.runOnUiThread {
                        try { activity.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) } catch (e: Exception) {}
                        activity.toast("浏览器下载好以后，用 Wear OS 工具箱安装 shuku-wear.apk")
                    }
                    return@execute
                }
                val f = fetch(info, "shuku-wear.apk")
                val uri = saveToDownloads(activity, f, "shuku-wear.apk")
                activity.runOnUiThread {
                    activity.toast("手表 App ${info.versionName} 已保存到“下载”文件夹")
                    val send = Intent(Intent.ACTION_SEND)
                        .setType("application/vnd.android.package-archive")
                        .putExtra(Intent.EXTRA_STREAM, uri)
                        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    try {
                        activity.startActivity(Intent.createChooser(send, "用 Wear OS 工具箱装到手表"))
                    } catch (e: Exception) {}
                }
            } catch (e: Exception) {
                activity.runOnUiThread { activity.toast("下载手表 App 失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                busy = false
            }
        }
    }

    /**
     * 系统安装器要用户确认。App 在前台就直接弹出来；不在前台时（下载期间切到了别的 App）系统会拦下，
     * 改成发一条通知，点通知再弹。通知也发不出来（没给通知权限）就等回到 App 时再弹
     */
    fun confirm(ctx: Context, confirm: Intent) {
        confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (MainActivity.visible) {
            try {
                ctx.startActivity(confirm)
                return
            } catch (e: Exception) {}
        }
        val nm = ctx.getSystemService(NotificationManager::class.java)
        val canNotify = nm != null && nm.areNotificationsEnabled() &&
            nm.getNotificationChannel(ShukuApp.CH_UPDATE)?.importance != NotificationManager.IMPORTANCE_NONE
        if (!canNotify) {
            pendingConfirm = confirm
            return
        }
        val pi = PendingIntent.getActivity(ctx, NOTIF_INSTALL, confirm, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val n = Notification.Builder(ctx, ShukuApp.CH_UPDATE)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentTitle("书库 App 新版本下载好了")
            .setContentText("点这里安装")
            .setContentIntent(pi)
            .setAutoCancel(true)
            .build()
        try { nm?.notify(NOTIF_INSTALL, n) } catch (e: Exception) { pendingConfirm = confirm }
    }

    /** 回到 App 时：有等着确认的安装（通知发不出来的那种）就弹出来 */
    fun showPendingInstall(activity: Activity) {
        val c = pendingConfirm ?: return
        pendingConfirm = null
        try { activity.startActivity(c) } catch (e: Exception) {}
    }

    /** 安装结束（装好、取消、失败）：收起“点这里安装”的通知 */
    fun installFinished(ctx: Context) {
        pendingConfirm = null
        try { ctx.getSystemService(NotificationManager::class.java)?.cancel(NOTIF_INSTALL) } catch (e: Exception) {}
    }

    /** 存到公共的“下载”文件夹（Android 10 起不需要存储权限），同名的旧文件先删掉 */
    @android.annotation.TargetApi(29)
    private fun saveToDownloads(ctx: Context, f: File, name: String): Uri {
        val resolver = ctx.contentResolver
        val coll = MediaStore.Downloads.EXTERNAL_CONTENT_URI
        try {
            resolver.delete(coll, "${MediaStore.Downloads.DISPLAY_NAME}=?", arrayOf(name))
        } catch (e: Exception) {}
        val values = ContentValues().apply {
            put(MediaStore.Downloads.DISPLAY_NAME, name)
            put(MediaStore.Downloads.MIME_TYPE, "application/vnd.android.package-archive")
            put(MediaStore.Downloads.IS_PENDING, 1)
        }
        val uri = resolver.insert(coll, values) ?: throw IOException("没法保存到“下载”文件夹")
        resolver.openOutputStream(uri)?.use { out -> f.inputStream().use { it.copyTo(out) } } ?: throw IOException("没法写入“下载”文件夹")
        values.clear()
        values.put(MediaStore.Downloads.IS_PENDING, 0)
        resolver.update(uri, values, null, null)
        return uri
    }
}

/** 系统安装器的结果：需要用户确认时弹出确认界面（App 不在前台时发通知） */
class InstallReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, -999)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val confirm: Intent? = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java) else intent.getParcelableExtra(Intent.EXTRA_INTENT)
                if (confirm != null) Updater.confirm(ctx, confirm)
            }
            PackageInstaller.STATUS_SUCCESS -> Updater.installFinished(ctx)
            PackageInstaller.STATUS_FAILURE_ABORTED -> {
                Updater.installFinished(ctx)
                Toast.makeText(ctx, "已取消更新", Toast.LENGTH_SHORT).show()
            }
            else -> {
                Updater.installFinished(ctx)
                val msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: ""
                Toast.makeText(ctx, "更新没有完成" + (if (msg.isNotEmpty()) "：$msg" else ""), Toast.LENGTH_LONG).show()
            }
        }
    }
}
