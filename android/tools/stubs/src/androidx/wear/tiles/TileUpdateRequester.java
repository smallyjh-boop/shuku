package androidx.wear.tiles;

import androidx.annotation.NonNull;

// 只用于本地类型检查的存根（tiles 1.4）
public interface TileUpdateRequester {
    void requestUpdate(@NonNull Class<? extends TileService> tileService);
}
