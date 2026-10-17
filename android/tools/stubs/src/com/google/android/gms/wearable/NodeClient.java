package com.google.android.gms.wearable;
import com.google.android.gms.tasks.Task;
import java.util.List;
public abstract class NodeClient {
    public abstract Task<List<Node>> getConnectedNodes();
    public abstract Task<Node> getLocalNode();
}
