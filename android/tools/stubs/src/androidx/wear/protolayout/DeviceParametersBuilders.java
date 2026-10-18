package androidx.wear.protolayout;

import androidx.annotation.NonNull;

// 只用于本地类型检查的存根（protolayout 1.2，api/1.2.0-beta01.txt）
public final class DeviceParametersBuilders {
    private DeviceParametersBuilders() {}

    public static final int DEVICE_PLATFORM_UNDEFINED = 0;
    public static final int DEVICE_PLATFORM_WEAR_OS = 1;
    public static final int SCREEN_SHAPE_UNDEFINED = 0;
    public static final int SCREEN_SHAPE_ROUND = 1;
    public static final int SCREEN_SHAPE_RECT = 2;

    public static final class DeviceParameters {
        DeviceParameters() {}
        public int getDevicePlatform() { throw new UnsupportedOperationException(); }
        public float getFontScale() { throw new UnsupportedOperationException(); }
        public float getScreenDensity() { throw new UnsupportedOperationException(); }
        public int getScreenHeightDp() { throw new UnsupportedOperationException(); }
        public int getScreenShape() { throw new UnsupportedOperationException(); }
        public int getScreenWidthDp() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public DeviceParameters build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setDevicePlatform(int devicePlatform) { return this; }
            @NonNull public Builder setFontScale(float fontScale) { return this; }
            @NonNull public Builder setScreenDensity(float screenDensity) { return this; }
            @NonNull public Builder setScreenHeightDp(int screenHeightDp) { return this; }
            @NonNull public Builder setScreenShape(int screenShape) { return this; }
            @NonNull public Builder setScreenWidthDp(int screenWidthDp) { return this; }
        }
    }
}
