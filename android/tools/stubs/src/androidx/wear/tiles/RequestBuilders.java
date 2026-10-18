package androidx.wear.tiles;

import androidx.annotation.NonNull;
import androidx.wear.protolayout.DeviceParametersBuilders;
import java.util.List;

// 只用于本地类型检查的存根（tiles 1.4，api/1.4.0-beta01.txt）
public final class RequestBuilders {
    private RequestBuilders() {}

    public static final class TileRequest {
        TileRequest() {}
        @NonNull public DeviceParametersBuilders.DeviceParameters getDeviceConfiguration() { throw new UnsupportedOperationException(); }
        public int getTileId() { throw new UnsupportedOperationException(); }
    }

    public static final class ResourcesRequest {
        ResourcesRequest() {}
        @NonNull public DeviceParametersBuilders.DeviceParameters getDeviceConfiguration() { throw new UnsupportedOperationException(); }
        @NonNull public List<String> getResourceIds() { throw new UnsupportedOperationException(); }
        public int getTileId() { throw new UnsupportedOperationException(); }
        @NonNull public String getVersion() { throw new UnsupportedOperationException(); }
    }
}
