package androidx.wear.protolayout;

import android.content.ComponentName;
import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import java.util.Map;

// 只用于本地类型检查的存根，签名照 androidx.wear.protolayout:protolayout 1.2 的 api/1.2.0-beta01.txt（只写了用到的部分）；正式构建用真正的库
public final class ActionBuilders {
    private ActionBuilders() {}

    @NonNull public static AndroidBooleanExtra booleanExtra(boolean value) { throw new UnsupportedOperationException(); }
    @NonNull public static AndroidDoubleExtra doubleExtra(double value) { throw new UnsupportedOperationException(); }
    @NonNull public static AndroidIntExtra intExtra(int value) { throw new UnsupportedOperationException(); }
    @NonNull public static AndroidLongExtra longExtra(long value) { throw new UnsupportedOperationException(); }
    @NonNull public static AndroidStringExtra stringExtra(@NonNull String value) { throw new UnsupportedOperationException(); }
    @NonNull public static LaunchAction launchAction(@NonNull ComponentName activityComponentName) { throw new UnsupportedOperationException(); }
    @NonNull public static LaunchAction launchAction(@NonNull ComponentName activityComponentName, @NonNull Map<String, AndroidExtra> intentExtras) { throw new UnsupportedOperationException(); }

    public interface Action {}

    public interface AndroidExtra {}

    public static final class AndroidBooleanExtra implements AndroidExtra {
        AndroidBooleanExtra() {}
        public boolean getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class AndroidDoubleExtra implements AndroidExtra {
        AndroidDoubleExtra() {}
        public double getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class AndroidIntExtra implements AndroidExtra {
        AndroidIntExtra() {}
        public int getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class AndroidLongExtra implements AndroidExtra {
        AndroidLongExtra() {}
        public long getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class AndroidStringExtra implements AndroidExtra {
        AndroidStringExtra() {}
        @NonNull public String getValue() { throw new UnsupportedOperationException(); }
    }

    public static final class AndroidActivity {
        AndroidActivity() {}
        @NonNull public String getClassName() { throw new UnsupportedOperationException(); }
        @NonNull public Map<String, AndroidExtra> getKeyToExtraMapping() { throw new UnsupportedOperationException(); }
        @NonNull public String getPackageName() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public Builder addKeyToExtraMapping(@NonNull String key, @NonNull AndroidExtra extra) { return this; }
            @NonNull public AndroidActivity build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setClassName(@NonNull String className) { return this; }
            @NonNull public Builder setPackageName(@NonNull String packageName) { return this; }
        }
    }

    public static final class LaunchAction implements Action {
        LaunchAction() {}
        @Nullable public AndroidActivity getAndroidActivity() { throw new UnsupportedOperationException(); }

        public static final class Builder {
            public Builder() {}
            @NonNull public LaunchAction build() { throw new UnsupportedOperationException(); }
            @NonNull public Builder setAndroidActivity(@NonNull AndroidActivity androidActivity) { return this; }
        }
    }
}
