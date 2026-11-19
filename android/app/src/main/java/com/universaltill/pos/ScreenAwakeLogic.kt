package com.universaltill.pos

/**
 * ut-docs#5620: when the till window asks Android to keep the display on.
 * While the app is in front the window carries FLAG_KEEP_SCREEN_ON until the
 * operator has been idle for the chosen number of minutes; then the flag is
 * dropped and the OS screen timeout takes over. 0 = never time out. Times are
 * SystemClock.elapsedRealtime() values (monotonic: a wall-clock change must
 * not keep a screen on or switch it off early).
 */
object ScreenAwakeLogic {
    const val DEFAULT_MINUTES = 120

    /** The choices offered in Settings (minutes; 0 = always on). */
    val CHOICES = listOf(0, 15, 30, 60, 120, 240, 480)

    fun normalize(minutes: Int): Int = if (minutes in CHOICES) minutes else DEFAULT_MINUTES

    fun keepAwake(minutes: Int, lastInteractionMs: Long, nowMs: Long): Boolean =
        minutes == 0 || nowMs - lastInteractionMs < minutes * 60_000L

    /** Milliseconds until the flag must drop; null when it never does. */
    fun remainingMs(minutes: Int, lastInteractionMs: Long, nowMs: Long): Long? =
        if (minutes == 0) null else maxOf(0L, minutes * 60_000L - (nowMs - lastInteractionMs))
}
