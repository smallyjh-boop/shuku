package com.google.common.util.concurrent;

import java.util.concurrent.Executor;
import java.util.concurrent.Future;

// 只用于本地类型检查的存根（com.google.guava:listenablefuture:1.0，Wear Tiles 带进来的）
public interface ListenableFuture<V> extends Future<V> {
    void addListener(Runnable listener, Executor executor);
}
