package com.universaltill.pos

/**
 * The pure decisions behind the in-app Android update (ut-docs#1541,
 * ut-docs#4312). No Android types on purpose: [MainActivity] supplies the
 * device facts, and these run as plain JVM unit tests (src/test).
 */
object UpdateLogic {
    const val ABI_ARM64 = "arm64-v8a"
    const val ABI_ARMV7 = "armeabi-v7a"

    /**
     * Which release APK this till downloads (ut-docs#1541).
     *
     * A per-ABI build always uses its own compile-time [ownUrl]. A universal
     * build moves to its device's own package, chosen from [perAbiUrls] — the
     * FIXED compile-time table (BuildConfig) — by [supportedAbis]`[0]`, the
     * device's primary ABI (`Build.SUPPORTED_ABIS`). Never from page input:
     * the bridge takes no argument. An ABI not in the table (an x86 emulator)
     * stays on the universal package rather than guessing.
     */
    fun updateApkUrl(
        isUniversal: Boolean,
        ownUrl: String,
        perAbiUrls: Map<String, String>,
        supportedAbis: List<String>,
    ): String {
        if (!isUniversal) return ownUrl
        val primary = supportedAbis.firstOrNull() ?: return ownUrl
        return perAbiUrls[primary] ?: ownUrl
    }

    enum class Verdict { OK, STALLED, TIMED_OUT }

    /**
     * ut-docs#4312: replaces the fixed 5-minute poll limit, which failed every
     * 150 MB download on ordinary shop Wi-Fi. A download fails only when its
     * byte count has not moved for [stallMs], or after [hardCapMs] in total
     * however it is moving.
     */
    class StallDetector(
        private val stallMs: Long,
        private val hardCapMs: Long,
        private val startMs: Long,
    ) {
        private var lastBytes = -1L
        private var lastProgressMs = startMs

        fun observe(
            bytes: Long,
            nowMs: Long,
        ): Verdict {
            if (nowMs - startMs >= hardCapMs) return Verdict.TIMED_OUT
            if (bytes > lastBytes) {
                // The first observation of a download that has already moved
                // counts as progress; one still at 0 bytes keeps the start time.
                if (lastBytes >= 0 || bytes > 0) lastProgressMs = nowMs
                lastBytes = bytes
            }
            return if (nowMs - lastProgressMs >= stallMs) Verdict.STALLED else Verdict.OK
        }
    }

    /**
     * ut-docs#4312: whether an APK already on disk can be installed instead of
     * downloading again. Every tap used to delete a finished 150 MB file and
     * start over. Reuse only a file that is provably the CURRENT release and
     * complete: its length equals [expectedLength] (from [expectedApkLength]:
     * the server's Content-Length, else the finished saved download's total;
     * null when neither is known), it parses as an
     * archive ([archiveVersionCode], null when it does not), and it is newer
     * than the running build.
     */
    fun canReuseDownloadedApk(
        fileLength: Long,
        expectedLength: Long?,
        archiveVersionCode: Long?,
        installedVersionCode: Long,
    ): Boolean {
        if (fileLength <= 0 || expectedLength == null || fileLength != expectedLength) return false
        return archiveVersionCode != null && archiveVersionCode > installedVersionCode
    }

    /**
     * ut-docs#4312: the length a complete APK must have, for
     * [canReuseDownloadedApk]. The server's Content-Length ([headLength]) wins
     * whenever HEAD answered — it is the CURRENT release. When HEAD failed
     * (offline, 429), fall back to DownloadManager's COLUMN_TOTAL_SIZE_BYTES
     * ([savedTotalBytes]) for the persisted download, but only if that
     * download finished ([savedSuccessful]); otherwise unknown (null), so a
     * complete file is not deleted and re-downloaded just because the size
     * check could not reach the server.
     */
    fun expectedApkLength(
        headLength: Long?,
        savedSuccessful: Boolean,
        savedTotalBytes: Long?,
    ): Long? {
        if (headLength != null) return headLength
        if (!savedSuccessful || savedTotalBytes == null || savedTotalBytes <= 0) return null
        return savedTotalBytes
    }

    /**
     * ut-docs#4312: the [StallDetector] start, on the monotonic clock
     * ([nowElapsedMs], `SystemClock.elapsedRealtime()`), for a download
     * enqueued at wall-clock [enqueuedWallMs] (persisted with its id). The
     * 60-minute hard cap counts from the enqueue, not from whichever Activity
     * is watching now. Wall clock for the persisted value because the
     * monotonic one restarts on reboot and DownloadManager downloads survive
     * it. Nothing persisted, or an enqueue time in the future (the clock was
     * set back), starts now.
     */
    fun detectorStartMs(
        nowElapsedMs: Long,
        nowWallMs: Long,
        enqueuedWallMs: Long?,
    ): Long {
        if (enqueuedWallMs == null || enqueuedWallMs > nowWallMs) return nowElapsedMs
        return nowElapsedMs - (nowWallMs - enqueuedWallMs)
    }

    /** Whole percent of [done] over [total], or null when the total is unknown. */
    fun percent(
        done: Long,
        total: Long,
    ): Int? {
        if (total <= 0) return null
        return ((done.coerceAtLeast(0) * 100) / total).coerceIn(0, 100).toInt()
    }

    /** True when progress moved into a new 10 % step (logged once per step). */
    fun crossedLogStep(
        previousPercent: Int,
        percent: Int,
    ): Boolean = percent / 10 > previousPercent / 10 || (previousPercent < 0 && percent >= 0)

    /**
     * The payload `window.utAndroidUpdate(...)` receives:
     * `{state, percent, error}`. Built by hand (org.json is a stub in JVM
     * unit tests); escapes everything a JS string literal cannot hold raw.
     */
    fun stateJson(
        state: String,
        percent: Int?,
        error: String?,
    ): String = """{"state":${jsonString(state)},"percent":${percent ?: "null"},"error":${error?.let { jsonString(it) } ?: "null"}}"""

    private fun jsonString(s: String): String {
        val b = StringBuilder("\"")
        for (c in s) {
            when {
                c == '"' -> b.append("\\\"")
                c == '\\' -> b.append("\\\\")
                c == '\n' -> b.append("\\n")
                c == '\r' -> b.append("\\r")
                c == '\t' -> b.append("\\t")
                c < ' ' || c == ' ' || c == ' ' -> b.append(String.format("\\u%04x", c.code))
                else -> b.append(c)
            }
        }
        return b.append('"').toString()
    }
}
