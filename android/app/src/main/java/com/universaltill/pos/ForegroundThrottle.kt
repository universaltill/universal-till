package com.universaltill.pos

/**
 * ut-docs#7097 T8: rate-limits the "app is in front again, check the cloud
 * credential now" call into the Go till ([mobile.Resume]). An Activity resumes
 * after every dialog or permission screen, and each call can mean one cloud
 * request, so calls inside [minIntervalMs] are swallowed. A swallowed call does
 * not move the window. [nowMs] is a monotonic clock (SystemClock.elapsedRealtime).
 */
class ForegroundThrottle(
    private val minIntervalMs: Long = 30_000L,
    private val nowMs: () -> Long,
) {
    private var last: Long? = null

    @Synchronized
    fun shouldFire(): Boolean {
        val t = nowMs()
        val l = last
        if (l != null && t >= l && t - l < minIntervalMs) return false
        last = t
        return true
    }

    @Synchronized
    fun reset() {
        last = null
    }
}
