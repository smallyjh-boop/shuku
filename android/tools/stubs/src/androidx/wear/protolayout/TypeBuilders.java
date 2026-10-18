package androidx.wear.protolayout;

// 只用于本地类型检查的存根（protolayout 1.2）：只有 setter 重载里出现的几个类型
public final class TypeBuilders {
    private TypeBuilders() {}

    public static final class BoolProp {
        BoolProp() {}
        public boolean getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class Int32Prop {
        Int32Prop() {}
        public int getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class StringProp {
        StringProp() {}
        public String getValue() { throw new UnsupportedOperationException(); }
    }
}
