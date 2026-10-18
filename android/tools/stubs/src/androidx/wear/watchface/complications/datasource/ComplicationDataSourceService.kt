// 只用于本地类型检查的存根，签名照 androidx.wear.watchface:watchface-complications-data-source 1.2 的 api/1.2.0-beta02.txt
// （只写了用到的部分）。这个库是 Kotlin 写的，存根也用 Kotlin。正式构建用真正的库
package androidx.wear.watchface.complications.datasource

import android.app.Service
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.IBinder
import android.os.RemoteException
import androidx.wear.watchface.complications.data.ComplicationData
import androidx.wear.watchface.complications.data.ComplicationType

public abstract class ComplicationDataSourceService : Service() {
    final override fun onBind(intent: Intent): IBinder? = null

    public abstract fun getPreviewData(type: ComplicationType): ComplicationData?

    public abstract fun onComplicationRequest(request: ComplicationRequest, listener: ComplicationRequestListener)

    public open fun onComplicationActivated(complicationInstanceId: Int, type: ComplicationType) {}

    public open fun onComplicationDeactivated(complicationInstanceId: Int) {}

    public interface ComplicationRequestListener {
        @Throws(RemoteException::class)
        public fun onComplicationData(complicationData: ComplicationData?)
    }

    public companion object {
        public const val ACTION_COMPLICATION_UPDATE_REQUEST: String = "android.support.wearable.complications.ACTION_COMPLICATION_UPDATE_REQUEST"
        public const val METADATA_KEY_SUPPORTED_TYPES: String = "android.support.wearable.complications.SUPPORTED_TYPES"
        public const val METADATA_KEY_UPDATE_PERIOD_SECONDS: String = "android.support.wearable.complications.UPDATE_PERIOD_SECONDS"
    }
}

public class ComplicationRequest(
    public val complicationInstanceId: Int,
    public val complicationType: ComplicationType,
    @get:JvmName("isImmediateResponseRequired") public val immediateResponseRequired: Boolean,
)

public interface ComplicationDataSourceUpdateRequester {
    public fun requestUpdateAll()
    public fun requestUpdate(vararg complicationInstanceIds: Int)

    public companion object {
        @JvmStatic
        public fun create(context: Context, complicationDataSourceComponent: ComponentName): ComplicationDataSourceUpdateRequester =
            throw UnsupportedOperationException()
    }
}
