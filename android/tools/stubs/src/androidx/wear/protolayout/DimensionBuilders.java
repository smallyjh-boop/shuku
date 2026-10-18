package androidx.wear.protolayout;

import androidx.annotation.NonNull;

// 只用于本地类型检查的存根（protolayout 1.2，api/1.2.0-beta01.txt）。各尺寸类实现的接口和真正的库一样，
// 哪个尺寸能传给哪个 setWidth/setHeight 才对得上
public final class DimensionBuilders {
    private DimensionBuilders() {}

    @NonNull public static DpProp dp(float valueDp) { throw new UnsupportedOperationException(); }
    @NonNull public static SpProp sp(float valueSp) { throw new UnsupportedOperationException(); }
    @NonNull public static EmProp em(int value) { throw new UnsupportedOperationException(); }
    @NonNull public static EmProp em(float value) { throw new UnsupportedOperationException(); }
    @NonNull public static ExpandedDimensionProp expand() { throw new UnsupportedOperationException(); }
    @NonNull public static WrappedDimensionProp wrap() { throw new UnsupportedOperationException(); }

    public interface ContainerDimension {}

    public interface ImageDimension {}

    public interface SpacerDimension {}

    public interface ExtensionDimension {}

    public interface PivotDimension {}

    public static final class DpProp implements ContainerDimension, ExtensionDimension, ImageDimension, PivotDimension, SpacerDimension {
        DpProp() {}
        public float getValue() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder(float staticValue) {}
            @NonNull public DpProp build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setValue(float value) { return this; }
        }
    }

    public static final class SpProp {
        SpProp() {}
        public float getValue() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public SpProp build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setValue(float value) { return this; }
        }
    }

    public static final class EmProp {
        EmProp() {}
        public float getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class ExpandedDimensionProp implements ContainerDimension, ImageDimension, SpacerDimension {
        ExpandedDimensionProp() {}
    }

    public static final class WrappedDimensionProp implements ContainerDimension {
        WrappedDimensionProp() {}
    }
}
