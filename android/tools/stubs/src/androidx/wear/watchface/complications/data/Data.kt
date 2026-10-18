// 只用于本地类型检查的存根，签名照 androidx.wear.watchface:watchface-complications-data 1.2 的 api/1.2.0-beta02.txt
// （只写了用到的部分）。这个库是 Kotlin 写的，存根也用 Kotlin：属性、伴生对象的用法和真正的库一样。正式构建用真正的库
package androidx.wear.watchface.complications.data

import android.app.PendingIntent
import android.content.res.Resources
import java.time.Instant

public enum class ComplicationType {
    NO_DATA,
    EMPTY,
    NOT_CONFIGURED,
    SHORT_TEXT,
    LONG_TEXT,
    RANGED_VALUE,
    MONOCHROMATIC_IMAGE,
    SMALL_IMAGE,
    PHOTO_IMAGE,
    NO_PERMISSION,
    GOAL_PROGRESS,
    WEIGHTED_ELEMENTS,
}

public interface ComplicationText {
    public fun getTextAt(resources: Resources, instant: Instant): CharSequence
    public fun returnsSameText(firstInstant: Instant, secondInstant: Instant): Boolean
    public fun getNextChangeTime(afterInstant: Instant): Instant
    public fun isAlwaysEmpty(): Boolean
}

public class PlainComplicationText internal constructor(private val text: CharSequence) : ComplicationText {
    override fun getTextAt(resources: Resources, instant: Instant): CharSequence = text
    override fun returnsSameText(firstInstant: Instant, secondInstant: Instant): Boolean = true
    override fun getNextChangeTime(afterInstant: Instant): Instant = Instant.MAX
    override fun isAlwaysEmpty(): Boolean = text.isEmpty()

    public class Builder(private val text: CharSequence) {
        public fun build(): PlainComplicationText = PlainComplicationText(text)
    }
}

public sealed class ComplicationData(public val type: ComplicationType, public val tapAction: PendingIntent?)

public class ShortTextComplicationData internal constructor(
    public val text: ComplicationText,
    public val title: ComplicationText?,
    public val contentDescription: ComplicationText?,
    tapAction: PendingIntent?,
) : ComplicationData(ComplicationType.SHORT_TEXT, tapAction) {
    public class Builder(private val text: ComplicationText, private val contentDescription: ComplicationText) {
        private var title: ComplicationText? = null
        private var tapAction: PendingIntent? = null
        public fun setTapAction(tapAction: PendingIntent?): Builder = apply { this.tapAction = tapAction }
        public fun setTitle(title: ComplicationText?): Builder = apply { this.title = title }
        public fun build(): ShortTextComplicationData = ShortTextComplicationData(text, title, contentDescription, tapAction)
    }
}

public class LongTextComplicationData internal constructor(
    public val text: ComplicationText,
    public val title: ComplicationText?,
    public val contentDescription: ComplicationText?,
    tapAction: PendingIntent?,
) : ComplicationData(ComplicationType.LONG_TEXT, tapAction) {
    public class Builder(private val text: ComplicationText, private val contentDescription: ComplicationText) {
        private var title: ComplicationText? = null
        private var tapAction: PendingIntent? = null
        public fun setTapAction(tapAction: PendingIntent?): Builder = apply { this.tapAction = tapAction }
        public fun setTitle(title: ComplicationText?): Builder = apply { this.title = title }
        public fun build(): LongTextComplicationData = LongTextComplicationData(text, title, contentDescription, tapAction)
    }
}

public class NoDataComplicationData public constructor(public val placeholder: ComplicationData?) :
    ComplicationData(ComplicationType.NO_DATA, null) {
    public constructor() : this(null)
}
