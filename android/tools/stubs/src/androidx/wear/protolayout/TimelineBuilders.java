package androidx.wear.protolayout;

import androidx.annotation.NonNull;

// 只用于本地类型检查的存根（protolayout 1.2，api/1.2.0-beta01.txt）
public final class TimelineBuilders {
    private TimelineBuilders() {}

    public static final class Timeline {
        Timeline() {}

        @NonNull
        public static Timeline fromLayoutElement(@NonNull LayoutElementBuilders.LayoutElement layoutElement) { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Timeline build() { throw new UnsupportedOperationException(); }
        }
    }
}
