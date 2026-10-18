package androidx.concurrent.futures;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import com.google.common.util.concurrent.ListenableFuture;
import java.util.concurrent.Executor;

// 只用于本地类型检查的存根，签名照 androidx.concurrent:concurrent-futures 1.1.0 的 api/1.1.0-rc01.txt；正式构建用真正的库
public final class CallbackToFutureAdapter {
    private CallbackToFutureAdapter() {}

    @NonNull
    public static <T> ListenableFuture<T> getFuture(@NonNull Resolver<T> callback) { throw new UnsupportedOperationException(); }

    public static final class Completer<T> {
        Completer() {}
        public boolean set(T value) { throw new UnsupportedOperationException(); }
        public boolean setException(@NonNull Throwable t) { throw new UnsupportedOperationException(); }
        public boolean setCancelled() { throw new UnsupportedOperationException(); }
        public void addCancellationListener(@NonNull Runnable runnable, @NonNull Executor executor) { throw new UnsupportedOperationException(); }
    }

    public interface Resolver<T> {
        @Nullable
        Object attachCompleter(@NonNull Completer<T> completer) throws Exception;
    }
}
