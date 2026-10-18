package androidx.wear.protolayout;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;

// 只用于本地类型检查的存根（protolayout 1.2，api/1.2.0-beta01.txt，只写了用到的修饰）
public final class ModifiersBuilders {
    private ModifiersBuilders() {}

    public static final int SEMANTICS_ROLE_NONE = 0;
    public static final int SEMANTICS_ROLE_IMAGE = 1;
    public static final int SEMANTICS_ROLE_BUTTON = 2;

    public static final class Modifiers {
        Modifiers() {}
        @Nullable public Background getBackground() { throw new UnsupportedOperationException(); }
        @Nullable public Clickable getClickable() { throw new UnsupportedOperationException(); }
        @Nullable public Padding getPadding() { throw new UnsupportedOperationException(); }
        @Nullable public Semantics getSemantics() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Modifiers build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setBackground(@NonNull Background background) { return this; }
            @NonNull public Builder setClickable(@NonNull Clickable clickable) { return this; }
            @NonNull public Builder setPadding(@NonNull Padding padding) { return this; }
            @NonNull public Builder setSemantics(@NonNull Semantics semantics) { return this; }
        }
    }

    public static final class Background {
        Background() {}
        @Nullable public ColorBuilders.ColorProp getColor() { throw new UnsupportedOperationException(); }
        @Nullable public Corner getCorner() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Background build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setColor(@NonNull ColorBuilders.ColorProp color) { return this; }
            @NonNull public Builder setCorner(@NonNull Corner corner) { return this; }
        }
    }

    public static final class Corner {
        Corner() {}
        @Nullable public DimensionBuilders.DpProp getRadius() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Corner build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setRadius(@NonNull DimensionBuilders.DpProp radius) { return this; }
        }
    }

    public static final class Clickable {
        Clickable() {}
        @NonNull public String getId() { throw new UnsupportedOperationException(); }
        @Nullable public ActionBuilders.Action getOnClick() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Clickable build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setId(@NonNull String id) { return this; }
            @NonNull public Builder setOnClick(@NonNull ActionBuilders.Action onClick) { return this; }
        }
    }

    public static final class Padding {
        Padding() {}
        @Nullable public DimensionBuilders.DpProp getStart() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Padding build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setAll(@NonNull DimensionBuilders.DpProp value) { return this; }
            @NonNull public Builder setBottom(@NonNull DimensionBuilders.DpProp bottom) { return this; }
            @NonNull public Builder setEnd(@NonNull DimensionBuilders.DpProp end) { return this; }
            @NonNull public Builder setRtlAware(@NonNull TypeBuilders.BoolProp rtlAware) { return this; }
            @NonNull public Builder setRtlAware(boolean rtlAware) { return this; }
            @NonNull public Builder setStart(@NonNull DimensionBuilders.DpProp start) { return this; }
            @NonNull public Builder setTop(@NonNull DimensionBuilders.DpProp top) { return this; }
        }
    }

    public static final class Semantics {
        Semantics() {}
        @Nullable public TypeBuilders.StringProp getContentDescription() { throw new UnsupportedOperationException(); }
        public int getRole() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Semantics build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setContentDescription(@NonNull TypeBuilders.StringProp contentDescription) { return this; }
            @NonNull public Builder setContentDescription(@NonNull String contentDescription) { return this; }
            @NonNull public Builder setRole(int role) { return this; }
        }
    }
}
