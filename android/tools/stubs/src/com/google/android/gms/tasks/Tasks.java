package com.google.android.gms.tasks;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
public final class Tasks {
    public static <TResult> TResult await(Task<TResult> task) throws ExecutionException, InterruptedException { throw new UnsupportedOperationException(); }
    public static <TResult> TResult await(Task<TResult> task, long timeout, TimeUnit unit) throws ExecutionException, InterruptedException, TimeoutException { throw new UnsupportedOperationException(); }
}
