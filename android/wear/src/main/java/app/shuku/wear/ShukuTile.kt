package app.shuku.wear

import android.content.ComponentName
import androidx.concurrent.futures.CallbackToFutureAdapter
import androidx.wear.protolayout.ActionBuilders
import androidx.wear.protolayout.ColorBuilders
import androidx.wear.protolayout.DeviceParametersBuilders
import androidx.wear.protolayout.DimensionBuilders
import androidx.wear.protolayout.LayoutElementBuilders
import androidx.wear.protolayout.ModifiersBuilders
import androidx.wear.protolayout.ResourceBuilders
import androidx.wear.protolayout.TimelineBuilders
import androidx.wear.tiles.RequestBuilders
import androidx.wear.tiles.TileBuilders
import androidx.wear.tiles.TileService
import com.google.common.util.concurrent.ListenableFuture
import kotlin.math.roundToInt

/**
 * 书库的 Tile（在表盘上左右滑就能看到）：今天读了多少分钟、正在读（听）的书，“继续读”“继续听”两个按钮。
 * 没连书库时提示去连接。数据见 Today：先用手表上存的马上显示，同时在后台问一次书库，变了再刷新。
 * 只用 ProtoLayout 最基础的元素（文字、方框、行、列、点击打开页面），老版本的 Wear OS 也能显示。
 * 点按钮打开的页面必须 exported（系统的 Tile 界面替我们打开）。
 */
class ShukuTile : TileService() {
    override fun onTileRequest(requestParams: RequestBuilders.TileRequest): ListenableFuture<TileBuilders.Tile> {
        val info = Today.info()
        Today.refresh()
        val tile = TileBuilders.Tile.Builder()
            .setResourcesVersion(RES_VERSION)
            // 半小时后系统再来要一次（过了零点“今天”要清零，别的设备上读了也要更新）
            .setFreshnessIntervalMillis(30 * 60_000L)
            .setTileTimeline(TimelineBuilders.Timeline.fromLayoutElement(layout(requestParams.deviceConfiguration, info)))
            .build()
        return CallbackToFutureAdapter.getFuture<TileBuilders.Tile> { c ->
            c.set(tile)
            "shuku-tile"
        }
    }

    // 没有图片，资源是空的
    override fun onTileResourcesRequest(requestParams: RequestBuilders.ResourcesRequest): ListenableFuture<ResourceBuilders.Resources> {
        val res = ResourceBuilders.Resources.Builder().setVersion(RES_VERSION).build()
        return CallbackToFutureAdapter.getFuture<ResourceBuilders.Resources> { c ->
            c.set(res)
            "shuku-tile-resources"
        }
    }

    private fun layout(dev: DeviceParametersBuilders.DeviceParameters, info: Today.Info): LayoutElementBuilders.LayoutElement {
        val round = dev.screenShape == DeviceParametersBuilders.SCREEN_SHAPE_ROUND
        // 圆表盘四周留白
        val side = if (round) dev.screenWidthDp * 0.1f else 8f
        val col = LayoutElementBuilders.Column.Builder()
            .setWidth(DimensionBuilders.expand())
            .setHorizontalAlignment(LayoutElementBuilders.HORIZONTAL_ALIGN_CENTER)
            .setModifiers(ModifiersBuilders.Modifiers.Builder()
                .setPadding(ModifiersBuilders.Padding.Builder().setStart(dp(side)).setEnd(dp(side)).build())
                .build())
        val book = info.book
        when {
            !info.configured -> {
                col.addContent(text("书库", 16f, Ui.INK, bold = true))
                col.addContent(space(6f))
                col.addContent(text("还没连接书库", 13f, Ui.INK2, lines = 2))
                col.addContent(space(10f))
                col.addContent(chip("去连接", true, open("connect", MainActivity::class.java, emptyMap())))
            }
            book == null -> {
                col.addContent(text(minutes(info.seconds), 12f, Ui.ACCENT))
                col.addContent(space(6f))
                col.addContent(text("还没有在读的书", 14f, Ui.INK, lines = 2))
                col.addContent(space(10f))
                col.addContent(chip("找书", true, open("home", MainActivity::class.java, emptyMap())))
            }
            else -> {
                col.addContent(text(minutes(info.seconds), 12f, Ui.ACCENT))
                col.addContent(space(4f))
                col.addContent(text(book.title.ifEmpty { "在读的书" }, 15f, Ui.INK, bold = true, lines = 2))
                val sub = sub(info, book)
                if (sub.isNotEmpty()) {
                    col.addContent(space(2f))
                    col.addContent(text(sub, 12f, Ui.INK2))
                }
                col.addContent(space(10f))
                val extras = mapOf<String, ActionBuilders.AndroidExtra>(
                    "id" to ActionBuilders.longExtra(book.id),
                    "title" to ActionBuilders.stringExtra(book.title),
                )
                val listenExtras = extras + (ListenActivity.EXTRA_RESUME to ActionBuilders.longExtra(book.id))
                col.addContent(LayoutElementBuilders.Row.Builder()
                    .setVerticalAlignment(LayoutElementBuilders.VERTICAL_ALIGN_CENTER)
                    .addContent(chip("继续读", false, open("read", ReadActivity::class.java, extras)))
                    .addContent(LayoutElementBuilders.Spacer.Builder().setWidth(dp(6f)).build())
                    .addContent(chip(if (info.playing) "正在听" else "继续听", true, open("listen", ListenActivity::class.java, listenExtras)))
                    .build())
            }
        }
        return LayoutElementBuilders.Box.Builder()
            .setWidth(DimensionBuilders.expand())
            .setHeight(DimensionBuilders.expand())
            .setHorizontalAlignment(LayoutElementBuilders.HORIZONTAL_ALIGN_CENTER)
            .setVerticalAlignment(LayoutElementBuilders.VERTICAL_ALIGN_CENTER)
            .addContent(col.build())
            .build()
    }

    private fun minutes(seconds: Int): String = when {
        seconds <= 0 -> "今天还没读"
        seconds < 60 -> "今天读了不到 1 分钟"
        else -> "今天读了 ${seconds / 60} 分钟"
    }

    private fun sub(info: Today.Info, book: Today.Book): String {
        val pct = if (book.percent >= 0) "${(book.percent * 100).roundToInt()}%" else ""
        val state = when {
            info.listening && info.playing -> "正在听"
            info.listening -> "听书暂停中"
            pct.isNotEmpty() -> "读到"
            else -> ""
        }
        return listOf(state, pct).filter { it.isNotEmpty() }.joinToString(" ")
    }

    private fun dp(v: Float) = DimensionBuilders.dp(v)

    private fun space(h: Float): LayoutElementBuilders.LayoutElement = LayoutElementBuilders.Spacer.Builder().setHeight(dp(h)).build()

    private fun text(s: String, sizeSp: Float, color: Int, bold: Boolean = false, lines: Int = 1): LayoutElementBuilders.LayoutElement =
        LayoutElementBuilders.Text.Builder()
            .setText(s)
            .setMaxLines(lines)
            .setMultilineAlignment(LayoutElementBuilders.TEXT_ALIGN_CENTER)
            .setOverflow(LayoutElementBuilders.TEXT_OVERFLOW_ELLIPSIZE_END)
            .setFontStyle(LayoutElementBuilders.FontStyle.Builder()
                .setSize(DimensionBuilders.sp(sizeSp))
                .setColor(ColorBuilders.argb(color))
                .setWeight(if (bold) LayoutElementBuilders.FONT_WEIGHT_BOLD else LayoutElementBuilders.FONT_WEIGHT_NORMAL)
                .build())
            .build()

    /** 圆角按钮：primary 用石青色底 */
    private fun chip(label: String, primary: Boolean, click: ModifiersBuilders.Clickable): LayoutElementBuilders.LayoutElement =
        LayoutElementBuilders.Box.Builder()
            .setWidth(DimensionBuilders.wrap())
            .setHeight(dp(40f))
            .setHorizontalAlignment(LayoutElementBuilders.HORIZONTAL_ALIGN_CENTER)
            .setVerticalAlignment(LayoutElementBuilders.VERTICAL_ALIGN_CENTER)
            .setModifiers(ModifiersBuilders.Modifiers.Builder()
                .setBackground(ModifiersBuilders.Background.Builder()
                    .setColor(ColorBuilders.argb(if (primary) Ui.ACCENT else Ui.CARD))
                    .setCorner(ModifiersBuilders.Corner.Builder().setRadius(dp(20f)).build())
                    .build())
                .setPadding(ModifiersBuilders.Padding.Builder().setStart(dp(12f)).setEnd(dp(12f)).build())
                .setClickable(click)
                .setSemantics(ModifiersBuilders.Semantics.Builder().setContentDescription(label).build())
                .build())
            .addContent(text(label, 13f, if (primary) Ui.BG else Ui.INK, bold = primary))
            .build()

    /** 点一下打开手表上的一个页面 */
    private fun open(id: String, cls: Class<*>, extras: Map<String, ActionBuilders.AndroidExtra>): ModifiersBuilders.Clickable =
        ModifiersBuilders.Clickable.Builder()
            .setId(id)
            .setOnClick(ActionBuilders.launchAction(ComponentName(this, cls), extras))
            .build()

    companion object {
        private const val RES_VERSION = "1"
    }
}
