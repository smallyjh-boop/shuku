package com.google.android.gms.wearable;
import android.app.Service;
import android.content.Intent;
import android.os.IBinder;
public abstract class WearableListenerService extends Service {
    public void onMessageReceived(MessageEvent messageEvent) {}
    @Override public final IBinder onBind(Intent intent) { return null; }
}
