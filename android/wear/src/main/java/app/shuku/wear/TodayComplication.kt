package app.shuku.wear

import android.app.PendingIntent
import android.content.Intent
import androidx.wear.watchface.complications.data.ComplicationData
import androidx.wear.watchface.complications.data.ComplicationText
import androidx.wear.watchface.complications.data.ComplicationType
import androidx.wear.watchface.complications.data.LongTextComplicationData
import androidx.wear.watchface.complications.data.NoDataComplicationData
import androidx.wear.watchface.complications.data.PlainComplicationText
import androidx.wear.watchface.complications.data.ShortTextComplicationData
import androidx.wear.watchface.complications.datasource.ComplicationDataSourceService
import androidx.wear.watchface.complications.datasource.ComplicationDataSourceService.ComplicationRequestListener
import androidx.wear.watchface.complications.datasource.ComplicationRequest

/**
 * 表盘复杂功能“今天读书”：短文字“N 分”（标题“今天”），长文字“今天读了 N 分钟”和书名。点一下接着听最近在读的书。
 * 先用手表上存的数马上回给表盘，同时在后台问一次书库（Today），变了再请表盘刷新。
 */
class TodayComplication : ComplicationDataSourceService() {
    override fun onComplicationRequest(request: ComplicationRequest, listener: ComplicationRequestListener) {
        val data = build(request.complicationType, Today.info(), tap())
        Today.refresh()
        try {
            listener.onComplicationData(data)
        } catch (e: Exception) {
            // 表盘那边已经断开了，不管
        }
    }

    // 表盘编辑器里的预览（固定的样子，在后台线程调用）
    override fun getPreviewData(type: ComplicationType): ComplicationData? =
        build(type, Today.Info(true, 25 * 60, Today.Book(0, "在读的书", 0.42), false, false), null)

    private fun build(type: ComplicationType, info: Today.Info, tap: PendingIntent?): ComplicationData {
        val m = info.seconds / 60
        val title = info.book?.title.orEmpty()
        return when (type) {
            ComplicationType.SHORT_TEXT ->
                if (info.configured) ShortTextComplicationData.Builder(plain("$m 分"), plain("今天读了 $m 分钟"))
                    .setTitle(plain("今天"))
                    .setTapAction(tap)
                    .build()
                else ShortTextComplicationData.Builder(plain("未连接"), plain("书库还没连接"))
                    .setTitle(plain("书库"))
                    .setTapAction(tap)
                    .build()
            ComplicationType.LONG_TEXT ->
                if (info.configured) LongTextComplicationData.Builder(plain("今天读了 $m 分钟"), plain("今天读了 $m 分钟 $title"))
                    .setTitle(if (title.isNotEmpty()) plain(title) else null)
                    .setTapAction(tap)
                    .build()
                else LongTextComplicationData.Builder(plain("书库还没连接"), plain("书库还没连接"))
                    .setTapAction(tap)
                    .build()
            // 只声明了短文字、长文字两种，别的类型不会来；万一来了回“没有数据”（类型必须对得上，否则会出错）
            else -> NoDataComplicationData()
        }
    }

    private fun plain(s: String): ComplicationText = PlainComplicationText.Builder(s).build()

    /** 点一下：接着听（听哪本由播放页按 Tile 的规则选）；没连书库就打开首页 */
    private fun tap(): PendingIntent {
        val i = if (WearApp.app.config.configured) Intent(this, ListenActivity::class.java).putExtra(ListenActivity.EXTRA_RESUME, -1L)
        else Intent(this, MainActivity::class.java)
        i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        return PendingIntent.getActivity(this, 31, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }
}
