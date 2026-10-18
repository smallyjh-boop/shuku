package androidx.wear.protolayout;

import androidx.annotation.NonNull;

// 只用于本地类型检查的存根（protolayout 1.2，api/1.2.0-beta01.txt）
public final class ColorBuilders {
    private ColorBuilders() {}

    @NonNull public static ColorProp argb(int colorArgb) { throw new UnsupportedOperationException(); }

    public static final class ColorProp {
        ColorProp() {}
        public int getArgb() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder(int staticValue) {}
            @NonNull public ColorProp build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setArgb(int argb) { return this; }
        }
    }
}
