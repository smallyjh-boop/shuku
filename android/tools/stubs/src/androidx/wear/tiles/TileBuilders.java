package androidx.wear.tiles;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.wear.protolayout.TimelineBuilders;

// 只用于本地类型检查的存根（tiles 1.4，api/1.4.0-beta01.txt）
public final class TileBuilders {
    private TileBuilders() {}

    public static final class Tile {
        Tile() {}
        public long getFreshnessIntervalMillis() { throw new UnsupportedOperationException(); }
        @NonNull public String getResourcesVersion() { throw new UnsupportedOperationException(); }
        @Nullable public TimelineBuilders.Timeline getTileTimeline() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Tile build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setFreshnessIntervalMillis(long freshnessIntervalMillis) { return this; }
            @NonNull public Builder setResourcesVersion(@NonNull String resourcesVersion) { return this; }
            @NonNull public Builder setTileTimeline(@NonNull TimelineBuilders.Timeline tileTimeline) { return this; }
        }
    }
}
