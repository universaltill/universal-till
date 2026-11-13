package com.universaltill.pos

/**
 * ut-docs#6153: the pure decisions behind the Android print bridge
 * (`window.AndroidKiosk.print()`). Kept free of Android types so they run on
 * the JVM, like [CaptureLogic].
 */
object PrintLogic {
    const val JOB_NAME_MAX = 64

    /** onFinish never came (an OEM spooler bug): allow printing again after this long. */
    const val IN_FLIGHT_STALE_MS = 120_000L

    private const val SEPARATOR = " – "

    /**
     * Untrusted page title to a print-job name: control characters become
     * spaces, whitespace is collapsed and trimmed, and "<app> – <title>" is
     * capped to [JOB_NAME_MAX] code points (never splitting a surrogate pair).
     * A null or blank title gives the app name alone.
     */
    fun jobName(appName: String, pageTitle: String?): String {
        val title = clean(pageTitle)
        val full = if (title.isEmpty()) clean(appName) else clean(appName) + SEPARATOR + title
        if (full.codePointCount(0, full.length) <= JOB_NAME_MAX) return full
        val end = full.offsetByCodePoints(0, JOB_NAME_MAX)
        return full.substring(0, end).trimEnd()
    }

    private fun clean(s: String?): String {
        if (s == null) return ""
        val sb = StringBuilder(s.length)
        var i = 0
        while (i < s.length) {
            val cp = s.codePointAt(i)
            i += Character.charCount(cp)
            if (Character.isISOControl(cp) || Character.isWhitespace(cp)) sb.append(' ') else sb.appendCodePoint(cp)
        }
        return sb.toString().replace(Regex(" {2,}"), " ").trim()
    }

    /** [inFlightSinceMs] 0 = none; otherwise in flight until [IN_FLIGHT_STALE_MS] has passed. */
    fun inFlight(inFlightSinceMs: Long, nowMs: Long): Boolean =
        inFlightSinceMs != 0L && nowMs - inFlightSinceMs < IN_FLIGHT_STALE_MS

    /** False when the kiosk is pinned, the activity is gone, or a dialog is already open. */
    fun canPrint(pinned: Boolean, activityGone: Boolean, inFlightSinceMs: Long, nowMs: Long): Boolean =
        !pinned && !activityGone && !inFlight(inFlightSinceMs, nowMs)
}
