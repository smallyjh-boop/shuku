package app.shuku

import android.app.AlertDialog
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
    private const val CHECK_EVERY = 6 * 3600 * 1000L
    private val app get() = ShukuApp.app
    @Volatile private var busy = false

    class Info(val versionCode: Int, val versionName: String, val url: String, val sha256: String)

    /** 书库上最新的安装包（kind：android 或 wear），书库里没有时返回 null */
    fun latest(kind: String): Info? {
        val j = Http.getJson(app.link.url("/api/clients"), null, 8000).optJSONObject(kind) ?: return null
        if (j.optString("url").isEmpty()) return null
        return Info(j.optInt("versionCode"), j.optString("versionName"), j.optString("url"), j.optString("sha256"))
    }

    /** 打开 App 时检查（最多 6 小时一次），有新版本就问要不要更新。启动时会有两处同时调用，加锁免得弹两次 */
    @Synchronized
    fun checkQuietly(activity: MainActivity) {
        val prefs = app.config.prefs
        val now = System.currentTimeMillis()
        if (busy || now - prefs.getLong("update_checked", 0) < CHECK_EVERY) return
        val info = try { latest("android") } catch (e: Exception) { null } ?: return
        prefs.edit().putLong("update_checked", now).apply()
        if (info.versionCode > BuildConfig.VERSION_CODE) activity.runOnUiThread { ask(activity, info) }
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
            .setNegativeButton("以后", null)
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
        app.io.execute {
            try {
                val f = fetch(info, "shuku-android.apk")
                installApk(activity.applicationContext, f)
            } catch (e: Exception) {
                activity.runOnUiThread { activity.toast("更新失败：${e.message ?: e.javaClass.simpleName}") }
            } finally {
                busy = false
            }
        }
    }

    /** 下载安装包到缓存目录，核对校验值 */
    private fun fetch(info: Info, name: String): File {
        val f = File(File(app.cacheDir, "update").apply { mkdirs() }, name)
        Http.download(app.link.url(info.url), null, f)
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

/** 系统安装器的结果：需要用户确认时弹出确认界面 */
class InstallReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, -999)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val confirm: Intent? = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java) else intent.getParcelableExtra(Intent.EXTRA_INTENT)
                if (confirm != null) {
                    confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                    try { ctx.startActivity(confirm) } catch (e: Exception) {}
                }
            }
            PackageInstaller.STATUS_SUCCESS -> {}
            PackageInstaller.STATUS_FAILURE_ABORTED -> Toast.makeText(ctx, "已取消更新", Toast.LENGTH_SHORT).show()
            else -> {
                val msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: ""
                Toast.makeText(ctx, "更新没有完成" + (if (msg.isNotEmpty()) "：$msg" else ""), Toast.LENGTH_LONG).show()
            }
        }
    }
}
