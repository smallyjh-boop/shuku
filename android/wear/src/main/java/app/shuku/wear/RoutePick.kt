package app.shuku.wear

/**
 * 同时探测几条线路时怎么选（不依赖 Android，单独测过）：候选按优先顺序排（内网在前）。
 * 排在前面的通了就用它；后面的先通了，前面还没结果的最多再等 grace 毫秒（在家时内网一般很快就回了），
 * 不用等前面的超时；都不通返回 NONE。每次调用传进各线路现在的状态。
 */
class RoutePick(private val grace: Long) {
    private var laterOkAt = -1L

    /** 后面的线路先通了时，最晚等到什么时候（没有这种情况是 Long.MAX_VALUE）：调用方据此决定最多睡多久 */
    val graceEndsAt: Long get() = if (laterOkAt < 0) Long.MAX_VALUE else laterOkAt + grace

    /** 返回选中的下标，或者 WAIT（再等等）、NONE（都不通） */
    fun step(states: IntArray, now: Long): Int {
        var waiting = false // 排在前面的还没有结果
        for (k in states.indices) {
            when (states[k]) {
                WAITING -> waiting = true
                OK -> {
                    if (!waiting) return k
                    if (laterOkAt < 0) laterOkAt = now
                    return if (now - laterOkAt >= grace) k else WAIT
                }
            }
        }
        return if (waiting) WAIT else NONE
    }

    companion object {
        const val WAITING = 0
        const val FAILED = 1
        const val OK = 2
        const val WAIT = -1
        const val NONE = -2
    }
}
