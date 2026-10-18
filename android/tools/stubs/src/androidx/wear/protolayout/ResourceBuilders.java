package androidx.wear.protolayout;

import androidx.annotation.NonNull;

// 只用于本地类型检查的存根（protolayout 1.2，api/1.2.0-beta01.txt）
public final class ResourceBuilders {
    private ResourceBuilders() {}

    public static final class Resources {
        Resources() {}
        @NonNull public String getVersion() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Resources build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setVersion(@NonNull String version) { return this; }
        }
    }
}
