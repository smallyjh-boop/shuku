package androidx.wear.protolayout;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import java.util.List;

// 只用于本地类型检查的存根（protolayout 1.2，api/1.2.0-beta01.txt，只写了用到的元素）。
// 用到的 setter 两种重载（int 常量、xxxProp）都写上，和真正的库一样，Kotlin 选重载时才不会有出入
public final class LayoutElementBuilders {
    private LayoutElementBuilders() {}

    public static final int HORIZONTAL_ALIGN_UNDEFINED = 0;
    public static final int HORIZONTAL_ALIGN_LEFT = 1;
    public static final int HORIZONTAL_ALIGN_CENTER = 2;
    public static final int HORIZONTAL_ALIGN_RIGHT = 3;
    public static final int HORIZONTAL_ALIGN_START = 4;
    public static final int HORIZONTAL_ALIGN_END = 5;
    public static final int VERTICAL_ALIGN_UNDEFINED = 0;
    public static final int VERTICAL_ALIGN_TOP = 1;
    public static final int VERTICAL_ALIGN_CENTER = 2;
    public static final int VERTICAL_ALIGN_BOTTOM = 3;
    public static final int TEXT_ALIGN_UNDEFINED = 0;
    public static final int TEXT_ALIGN_START = 1;
    public static final int TEXT_ALIGN_CENTER = 2;
    public static final int TEXT_ALIGN_END = 3;
    public static final int TEXT_OVERFLOW_UNDEFINED = 0;
    public static final int TEXT_OVERFLOW_TRUNCATE = 1;
    /** @deprecated 真正的库里也标了过时（新的是 TEXT_OVERFLOW_ELLIPSIZE，要新版 Tile 渲染器才认），老手表也能用这个 */
    @Deprecated public static final int TEXT_OVERFLOW_ELLIPSIZE_END = 2;
    public static final int TEXT_OVERFLOW_ELLIPSIZE = 4;
    public static final int FONT_WEIGHT_UNDEFINED = 0;
    public static final int FONT_WEIGHT_NORMAL = 400;
    public static final int FONT_WEIGHT_BOLD = 700;

    public interface LayoutElement {}

    public static final class HorizontalAlignmentProp {
        HorizontalAlignmentProp() {}
        public int getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class VerticalAlignmentProp {
        VerticalAlignmentProp() {}
        public int getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class TextAlignmentProp {
        TextAlignmentProp() {}
        public int getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class TextOverflowProp {
        TextOverflowProp() {}
        public int getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class FontWeightProp {
        FontWeightProp() {}
        public int getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class Box implements LayoutElement {
        Box() {}
        @NonNull public List<LayoutElement> getContents() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Builder addContent(@NonNull LayoutElement content) { return this; }
            @NonNull public Box build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setHeight(@NonNull DimensionBuilders.ContainerDimension height) { return this; }
            @NonNull public Builder setHorizontalAlignment(@NonNull HorizontalAlignmentProp horizontalAlignment) { return this; }
            @NonNull public Builder setHorizontalAlignment(int horizontalAlignment) { return this; }
            @NonNull public Builder setModifiers(@NonNull ModifiersBuilders.Modifiers modifiers) { return this; }
            @NonNull public Builder setVerticalAlignment(@NonNull VerticalAlignmentProp verticalAlignment) { return this; }
            @NonNull public Builder setVerticalAlignment(int verticalAlignment) { return this; }
            @NonNull public Builder setWidth(@NonNull DimensionBuilders.ContainerDimension width) { return this; }
        }
    }

    public static final class Column implements LayoutElement {
        Column() {}
        @NonNull public List<LayoutElement> getContents() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Builder addContent(@NonNull LayoutElement content) { return this; }
            @NonNull public Column build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setHeight(@NonNull DimensionBuilders.ContainerDimension height) { return this; }
            @NonNull public Builder setHorizontalAlignment(@NonNull HorizontalAlignmentProp horizontalAlignment) { return this; }
            @NonNull public Builder setHorizontalAlignment(int horizontalAlignment) { return this; }
            @NonNull public Builder setModifiers(@NonNull ModifiersBuilders.Modifiers modifiers) { return this; }
            @NonNull public Builder setWidth(@NonNull DimensionBuilders.ContainerDimension width) { return this; }
        }
    }

    public static final class Row implements LayoutElement {
        Row() {}
        @NonNull public List<LayoutElement> getContents() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Builder addContent(@NonNull LayoutElement content) { return this; }
            @NonNull public Row build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setHeight(@NonNull DimensionBuilders.ContainerDimension height) { return this; }
            @NonNull public Builder setModifiers(@NonNull ModifiersBuilders.Modifiers modifiers) { return this; }
            @NonNull public Builder setVerticalAlignment(@NonNull VerticalAlignmentProp verticalAlignment) { return this; }
            @NonNull public Builder setVerticalAlignment(int verticalAlignment) { return this; }
            @NonNull public Builder setWidth(@NonNull DimensionBuilders.ContainerDimension width) { return this; }
        }
    }

    public static final class Spacer implements LayoutElement {
        Spacer() {}
        @Nullable public DimensionBuilders.SpacerDimension getHeight() { throw new UnsupportedOperationException(); }
        @Nullable public DimensionBuilders.SpacerDimension getWidth() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Spacer build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setHeight(@NonNull DimensionBuilders.SpacerDimension height) { return this; }
            @NonNull public Builder setModifiers(@NonNull ModifiersBuilders.Modifiers modifiers) { return this; }
            @NonNull public Builder setWidth(@NonNull DimensionBuilders.SpacerDimension width) { return this; }
        }
    }

    public static final class FontStyle {
        FontStyle() {}
        @Nullable public DimensionBuilders.SpProp getSize() { throw new UnsupportedOperationException(); }
        @Nullable public ColorBuilders.ColorProp getColor() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public FontStyle build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setColor(@NonNull ColorBuilders.ColorProp color) { return this; }
            @NonNull public Builder setItalic(@NonNull TypeBuilders.BoolProp italic) { return this; }
            @NonNull public Builder setItalic(boolean italic) { return this; }
            @NonNull public Builder setLetterSpacing(@NonNull DimensionBuilders.EmProp letterSpacing) { return this; }
            @NonNull public Builder setSize(@NonNull DimensionBuilders.SpProp size) { return this; }
            @NonNull public Builder setUnderline(@NonNull TypeBuilders.BoolProp underline) { return this; }
            @NonNull public Builder setUnderline(boolean underline) { return this; }
            @NonNull public Builder setWeight(@NonNull FontWeightProp weight) { return this; }
            @NonNull public Builder setWeight(int weight) { return this; }
        }
    }

    public static final class Text implements LayoutElement {
        Text() {}
        @Nullable public TypeBuilders.StringProp getText() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Text build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setFontStyle(@NonNull FontStyle fontStyle) { return this; }
            @NonNull public Builder setLineHeight(@NonNull DimensionBuilders.SpProp lineHeight) { return this; }
            @NonNull public Builder setMaxLines(@NonNull TypeBuilders.Int32Prop maxLines) { return this; }
            @NonNull public Builder setMaxLines(int maxLines) { return this; }
            @NonNull public Builder setModifiers(@NonNull ModifiersBuilders.Modifiers modifiers) { return this; }
            @NonNull public Builder setMultilineAlignment(@NonNull TextAlignmentProp multilineAlignment) { return this; }
            @NonNull public Builder setMultilineAlignment(int multilineAlignment) { return this; }
            @NonNull public Builder setOverflow(@NonNull TextOverflowProp overflow) { return this; }
            @NonNull public Builder setOverflow(int overflow) { return this; }
            @NonNull public Builder setText(@NonNull TypeBuilders.StringProp text) { return this; }
            @NonNull public Builder setText(@NonNull String text) { return this; }
        }
    }
}
