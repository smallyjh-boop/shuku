package androidx.wear.tiles;

import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.os.IBinder;
import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import com.google.common.util.concurrent.ListenableFuture;

// 只用于本地类型检查的存根，签名照 androidx.wear.tiles:tiles 1.4 的 api/1.4.0-beta01.txt（只写了用到的部分）；正式构建用真正的库
public abstract class TileService extends Service {
    public static final String ACTION_BIND_TILE_PROVIDER = "androidx.wear.tiles.action.BIND_TILE_PROVIDER";
    public static final String EXTRA_CLICKABLE_ID = "androidx.wear.tiles.extra.CLICKABLE_ID";
    public static final String METADATA_PREVIEW_KEY = "androidx.wear.tiles.PREVIEW";

    public TileService() {}

    @NonNull
    public static TileUpdateRequester getUpdater(@NonNull Context context) { throw new UnsupportedOperationException(); }

    @Override
    @Nullable
    public IBinder onBind(@NonNull Intent intent) { return null; }

    @NonNull
    protected abstract ListenableFuture<TileBuilders.Tile> onTileRequest(@NonNull RequestBuilders.TileRequest requestParams);

    @NonNull
    protected ListenableFuture<androidx.wear.protolayout.ResourceBuilders.Resources> onTileResourcesRequest(@NonNull RequestBuilders.ResourcesRequest requestParams) {
        throw new UnsupportedOperationException();
    }
}
