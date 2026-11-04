package com.universaltill.pos

/** ut-docs#5436 / ADR-0179: the verdict on an APK about to go to the installer. */
sealed interface ApkCheck {
    /** Pinned signer matched. */
    data class Accept(val digest: String) : ApkCheck

    /** Dormant: no pin compiled in (package and version checks still passed). */
    object AcceptUnpinned : ApkCheck

    /**
     * [cls] is the closed class reported to the fleet roster (ut-docs#5446);
     * [reason] is the detailed text for logcat and the page only and must
     * never leave the device (it carries digests and foreign package names).
     */
    data class Refuse(val cls: RefuseClass, val reason: String) : ApkCheck
}

/**
 * ut-docs#5446: why an update APK was refused. [wire] is what
 * `Mobile.reportAndroidUpdateRefused` sends; it mirrors the closed set in Go's
 * internal/androidupdate (`classes`), which pins it with a test.
 */
enum class RefuseClass(val wire: String) {
    UNREADABLE_APK("unreadable_apk"),
    WRONG_PACKAGE("wrong_package"),
    NOT_NEWER("not_newer"),
    SIGNER_UNREADABLE("signer_unreadable"),
    MULTIPLE_SIGNERS("multiple_signers"),
    SIGNER_NOT_PINNED("signer_not_pinned"),
    CHECK_ERROR("check_error"),
}

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

    /** ut-docs#5436: the pin set compiled into BuildConfig.APK_SIGNER_SHA256S ("" = dormant). */
    fun parsePinSet(raw: String): Set<String> =
        raw.split(',').map { it.trim().lowercase() }.filter { it.isNotEmpty() }.toSet()

    /** Lowercase hex SHA-256 of [der] (a certificate's DER bytes = Signature.toByteArray()). */
    fun sha256Hex(der: ByteArray): String =
        java.security.MessageDigest.getInstance("SHA-256").digest(der).joinToString("") { "%02x".format(it) }

    /**
     * ut-docs#5436 / ADR-0179: whether the APK about to go to the installer
     * may go. The first failing rule wins; its reason is logged and passed to
     * failUpdate. Package and version are checked even when dormant.
     * [signerHistory] (API 28+, single signer only) is the rotation lineage.
     */
    fun checkUpdateApk(
        archivePackage: String?,
        ownPackage: String,
        archiveVersionCode: Long?,
        installedVersionCode: Long,
        signerDigests: List<String>?,
        pins: Set<String>,
        signerHistory: List<String>? = null,
    ): ApkCheck {
        if (archivePackage == null) return ApkCheck.Refuse(RefuseClass.UNREADABLE_APK, "not a readable APK")
        if (archivePackage != ownPackage) return ApkCheck.Refuse(RefuseClass.WRONG_PACKAGE, "package $archivePackage, expected $ownPackage")
        if (archiveVersionCode == null || archiveVersionCode <= installedVersionCode) {
            return ApkCheck.Refuse(RefuseClass.NOT_NEWER, "versionCode $archiveVersionCode not newer than $installedVersionCode")
        }
        if (pins.isEmpty()) return ApkCheck.AcceptUnpinned
        if (signerDigests.isNullOrEmpty()) return ApkCheck.Refuse(RefuseClass.SIGNER_UNREADABLE, "signer unreadable")
        if (signerDigests.size > 1) return ApkCheck.Refuse(RefuseClass.MULTIPLE_SIGNERS, "multiple signers")
        val d = signerDigests[0].lowercase()
        if (d in pins) return ApkCheck.Accept(d)
        // API 28+ key rotation: the platform-verified proof-of-rotation lineage
        // (signingCertificateHistory) containing a pinned key means the pinned
        // key vouched for this signer. Only reached for a single signer.
        val ancestor = signerHistory?.map { it.lowercase() }?.firstOrNull { it in pins }
        if (ancestor != null) return ApkCheck.Accept(ancestor)
        return ApkCheck.Refuse(RefuseClass.SIGNER_NOT_PINNED, "signer $d not pinned")
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
