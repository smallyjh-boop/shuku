package com.google.android.gms.wearable;
import com.google.android.gms.tasks.Task;
public abstract class MessageClient {
    public abstract Task<Integer> sendMessage(String nodeId, String path, byte[] data);
    public abstract Task<Void> addListener(OnMessageReceivedListener listener);
    public abstract Task<Boolean> removeListener(OnMessageReceivedListener listener);
    public interface OnMessageReceivedListener { void onMessageReceived(MessageEvent messageEvent); }
}
