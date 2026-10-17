package com.google.android.gms.tasks;
// 只用于本地类型检查的存根，签名和 Google Play 服务一致；正式构建用真正的库
public abstract class Task<TResult> {
    public abstract boolean isComplete();
    public abstract boolean isSuccessful();
    public abstract TResult getResult();
    public abstract Exception getException();
    public abstract Task<TResult> addOnSuccessListener(OnSuccessListener<? super TResult> listener);
    public abstract Task<TResult> addOnFailureListener(OnFailureListener listener);
    public Task<TResult> addOnCompleteListener(OnCompleteListener<TResult> listener) { throw new UnsupportedOperationException(); }
}
