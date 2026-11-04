package com.universaltill.pos

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * ut-docs#1541 / #4312: the pure decisions behind the in-app Android update,
 * kept free of Android types so they run on the JVM (`./gradlew
 * testArm64DebugUnitTest`) without a device.
 */
class UpdateLogicTest {
    private val table =
        mapOf(
            UpdateLogic.ABI_ARM64 to "https://example.test/arm64-latest.apk",
            UpdateLogic.ABI_ARMV7 to "https://example.test/armv7-latest.apk",
        )
    private val universalUrl = "https://example.test/android-latest.apk"

    // --- ABI -> URL (#1541) ---------------------------------------------------

    @Test
    fun perAbiBuildAlwaysKeepsItsOwnUrl() {
        val own = "https://example.test/armv7-latest.apk"
        // Even on a 64-bit device: an armv7 build installed there stays on
        // armv7 (its versionCode ranks below arm64, but moving it is not this
        // build's call — only the universal build migrates).
        assertEquals(own, UpdateLogic.updateApkUrl(false, own, table, listOf("arm64-v8a", "armeabi-v7a")))
    }

    @Test
    fun universalBuildMovesToTheDevicesPrimaryAbi() {
        assertEquals(table[UpdateLogic.ABI_ARM64], UpdateLogic.updateApkUrl(true, universalUrl, table, listOf("arm64-v8a", "armeabi-v7a", "armeabi")))
        assertEquals(table[UpdateLogic.ABI_ARMV7], UpdateLogic.updateApkUrl(true, universalUrl, table, listOf("armeabi-v7a", "armeabi")))
    }

    @Test
    fun universalBuildOnAnUnknownAbiStaysUniversal() {
        // x86_64 emulator, or a device reporting nothing: never guess.
        assertEquals(universalUrl, UpdateLogic.updateApkUrl(true, universalUrl, table, listOf("x86_64", "arm64-v8a")))
        assertEquals(universalUrl, UpdateLogic.updateApkUrl(true, universalUrl, table, emptyList()))
    }

    @Test
    fun onlyThePrimaryAbiCounts() {
        // A secondary arm64 entry behind x86_64 (houdini translation) is not
        // the device's own ABI.
        assertEquals(universalUrl, UpdateLogic.updateApkUrl(true, universalUrl, table, listOf("x86_64", "arm64-v8a")))
    }

    // --- stall detection (#4312) ---------------------------------------------

    @Test
    fun progressKeepsTheDownloadAliveWellPastTheOldFiveMinuteLimit() {
        val d = UpdateLogic.StallDetector(stallMs = 180_000, hardCapMs = 3_600_000, startMs = 0)
        var bytes = 0L
        // A slow 10-minute download that moves a little every 30 s.
        for (t in 0L..600_000L step 30_000L) {
            bytes += 1_000
            assertEquals(UpdateLogic.Verdict.OK, d.observe(bytes, t))
        }
    }

    @Test
    fun noByteProgressForThreeMinutesIsAStall() {
        val d = UpdateLogic.StallDetector(stallMs = 180_000, hardCapMs = 3_600_000, startMs = 0)
        assertEquals(UpdateLogic.Verdict.OK, d.observe(500, 10_000))
        assertEquals(UpdateLogic.Verdict.OK, d.observe(500, 189_999))
        assertEquals(UpdateLogic.Verdict.STALLED, d.observe(500, 190_000))
    }

    @Test
    fun aDownloadThatNeverStartsStallsFromItsStart() {
        val d = UpdateLogic.StallDetector(stallMs = 180_000, hardCapMs = 3_600_000, startMs = 1_000)
        assertEquals(UpdateLogic.Verdict.OK, d.observe(0, 180_999))
        assertEquals(UpdateLogic.Verdict.STALLED, d.observe(0, 181_000))
    }

    @Test
    fun hardCapEndsEvenAMovingDownload() {
        val d = UpdateLogic.StallDetector(stallMs = 180_000, hardCapMs = 3_600_000, startMs = 0)
        var bytes = 0L
        var t = 0L
        while (t < 3_600_000L) {
            bytes += 1
            assertEquals(UpdateLogic.Verdict.OK, d.observe(bytes, t))
            t += 60_000
        }
        assertEquals(UpdateLogic.Verdict.TIMED_OUT, d.observe(bytes + 1, 3_600_000))
    }

    // --- complete-APK reuse (#4312) ------------------------------------------

    @Test
    fun reusesACompleteNewerApkMatchingTheServerSize() {
        assertTrue(UpdateLogic.canReuseDownloadedApk(150_000_000, 150_000_000, 350_012, 340_012))
    }

    @Test
    fun neverReusesAPartialOrAStaleRelease() {
        // Partial: shorter than the server's Content-Length.
        assertFalse(UpdateLogic.canReuseDownloadedApk(80_000_000, 150_000_000, 350_012, 340_012))
        // A finished APK of an OLDER release (a newer one is now latest): size differs.
        assertFalse(UpdateLogic.canReuseDownloadedApk(149_000_000, 150_000_000, 350_012, 340_012))
        // Server size unknown (offline HEAD): cannot prove it is the latest.
        assertFalse(UpdateLogic.canReuseDownloadedApk(150_000_000, null, 350_012, 340_012))
        // Unparseable archive (truncated / corrupt).
        assertFalse(UpdateLogic.canReuseDownloadedApk(150_000_000, 150_000_000, null, 340_012))
        // The build already running (or older): installing it is a no-op at best.
        assertFalse(UpdateLogic.canReuseDownloadedApk(150_000_000, 150_000_000, 340_012, 340_012))
        // No file at all.
        assertFalse(UpdateLogic.canReuseDownloadedApk(0, 0, 350_012, 340_012))
    }

    @Test
    fun expectedLengthPrefersTheServerThenAFinishedSavedDownload() {
        // The server's Content-Length wins whenever HEAD answered: it is the
        // CURRENT release, even if the saved download was an older one.
        assertEquals(150_000_000L, UpdateLogic.expectedApkLength(150_000_000, true, 149_000_000))
        // HEAD failed (offline, 429): DownloadManager's own total for the
        // saved, SUCCESSFUL download proves the file on disk is complete.
        assertEquals(150_000_000L, UpdateLogic.expectedApkLength(null, true, 150_000_000))
        // ...but never for a download that did not finish, or with no total.
        assertNull(UpdateLogic.expectedApkLength(null, false, 150_000_000))
        assertNull(UpdateLogic.expectedApkLength(null, true, -1))
        assertNull(UpdateLogic.expectedApkLength(null, true, 0))
        assertNull(UpdateLogic.expectedApkLength(null, true, null))
    }

    @Test
    fun offlineTapReusesACompleteFileProvenByTheSavedDownload() {
        // HEAD failed; the saved download finished at exactly this length and
        // the file parses as a newer build -> install it, no 150 MB re-download.
        val expected = UpdateLogic.expectedApkLength(null, true, 150_000_000)
        assertTrue(UpdateLogic.canReuseDownloadedApk(150_000_000, expected, 350_012, 340_012))
        // Same, but the file on disk is shorter than what DownloadManager finished.
        assertFalse(UpdateLogic.canReuseDownloadedApk(80_000_000, expected, 350_012, 340_012))
        // Same, but the file is not newer than the running build.
        assertFalse(UpdateLogic.canReuseDownloadedApk(150_000_000, expected, 340_012, 340_012))
    }

    // --- hard cap across Activities (#4312) -----------------------------------

    @Test
    fun detectorStartsFromThePersistedEnqueueTime() {
        // Enqueued 50 minutes ago (wall clock); the detector's start is moved
        // back by the same 50 minutes on the monotonic clock, so a recreated
        // Activity does not get a fresh 60-minute cap.
        val start = UpdateLogic.detectorStartMs(nowElapsedMs = 1_000_000_000, nowWallMs = 5_000_000_000, enqueuedWallMs = 5_000_000_000 - 50 * 60_000)
        assertEquals(1_000_000_000L - 50 * 60_000, start)
        val d = UpdateLogic.StallDetector(stallMs = 10 * 60_000, hardCapMs = 60 * 60_000, startMs = start)
        assertEquals(UpdateLogic.Verdict.OK, d.observe(100, 1_000_000_000))
        assertEquals(UpdateLogic.Verdict.TIMED_OUT, d.observe(200, 1_000_000_000 + 10 * 60_000))
    }

    @Test
    fun detectorStartsNowWithoutAUsableEnqueueTime() {
        // Nothing persisted (a download from an older build).
        assertEquals(42L, UpdateLogic.detectorStartMs(42, 5_000_000_000, null))
        // Enqueue time in the future: the wall clock was set back; trust nothing.
        assertEquals(42L, UpdateLogic.detectorStartMs(42, 5_000_000_000, 5_000_060_000))
    }

    // --- progress + page report ----------------------------------------------

    @Test
    fun percentIsBoundedAndUnknownWithoutATotal() {
        assertNull(UpdateLogic.percent(10, -1))
        assertNull(UpdateLogic.percent(10, 0))
        assertEquals(0, UpdateLogic.percent(0, 100))
        assertEquals(42, UpdateLogic.percent(42, 100))
        assertEquals(100, UpdateLogic.percent(150, 100))
    }

    @Test
    fun logsEveryTenPercentStepOnlyOnce() {
        assertTrue(UpdateLogic.crossedLogStep(-1, 0))
        assertFalse(UpdateLogic.crossedLogStep(0, 9))
        assertTrue(UpdateLogic.crossedLogStep(9, 10))
        assertFalse(UpdateLogic.crossedLogStep(10, 19))
        assertTrue(UpdateLogic.crossedLogStep(19, 35))
    }

    @Test
    fun stateJsonIsValidAndEscaped() {
        assertEquals(
            """{"state":"downloading","percent":42,"error":null}""",
            UpdateLogic.stateJson("downloading", 42, null),
        )
        assertEquals(
            // U+2028 is legal raw in JSON but ends a pre-ES2019 JS string
            // literal, so it must arrive escaped. The input ends in a literal
            // U+2028 character; the expected output is the six characters
            // backslash-u-2-0-2-8, spelled "\\u2028" in an ordinary (escaped)
            // string so it cannot be mistaken for the raw character.
            """{"state":"failed","percent":null,"error":"bad \"thing\"\\ \n </script> """ + "\\u2028\"}",
            UpdateLogic.stateJson("failed", null, "bad \"thing\"\\ \n </script>  "),
        )
    }

    // --- APK signer pin (ut-docs#5436 / ADR-0179) ------------------------------

    private val own = "com.universaltill.pos"
    private val a = "a".repeat(64)
    private val b = "b".repeat(64)

    private fun check(
        pkg: String? = own,
        versionCode: Long? = 20L,
        signers: List<String>? = listOf(a),
        pins: Set<String> = setOf(a),
    ) = UpdateLogic.checkUpdateApk(pkg, own, versionCode, 10L, signers, pins)

    private fun reason(c: ApkCheck): String = (c as ApkCheck.Refuse).reason

    @Test
    fun parsePinSetTrimsLowercasesAndDropsBlanks() {
        assertEquals(emptySet<String>(), UpdateLogic.parsePinSet(""))
        assertEquals(setOf("aa", "bb"), UpdateLogic.parsePinSet(" AA , BB,,"))
    }

    @Test
    fun sha256HexMatchesTheKnownVector() {
        assertEquals(
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
            UpdateLogic.sha256Hex("abc".toByteArray()),
        )
    }

    @Test
    fun pinnedSignerIsAccepted() {
        assertEquals(ApkCheck.Accept(a), check())
    }

    @Test
    fun unpinnedSignerIsRefused() {
        assertTrue(reason(check(signers = listOf(b))).contains("not pinned"))
    }

    @Test
    fun unreadableOrOtherPackageIsRefused() {
        assertTrue(check(pkg = null) is ApkCheck.Refuse)
        assertTrue(reason(check(pkg = "com.evil.app")).contains("com.evil.app"))
        // Even dormant, another package never goes through.
        assertTrue(check(pkg = "com.evil.app", pins = emptySet()) is ApkCheck.Refuse)
    }

    @Test
    fun versionCodeMustBeNewer() {
        assertTrue(check(versionCode = 10L) is ApkCheck.Refuse)
        assertTrue(check(versionCode = 9L) is ApkCheck.Refuse)
        assertTrue(check(versionCode = null) is ApkCheck.Refuse)
    }

    @Test
    fun multipleOrMissingSignersAreRefused() {
        assertTrue(reason(check(signers = listOf(a, b))).contains("multiple"))
        assertTrue(check(signers = emptyList()) is ApkCheck.Refuse)
        assertTrue(check(signers = null) is ApkCheck.Refuse)
    }

    @Test
    fun anyPinInTheSetIsAccepted() {
        assertEquals(ApkCheck.Accept(b), check(signers = listOf(b), pins = setOf(a, b)))
    }

    @Test
    fun emptyPinSetIsDormant() {
        assertEquals(ApkCheck.AcceptUnpinned, check(pins = emptySet()))
        assertEquals(ApkCheck.AcceptUnpinned, check(pins = emptySet(), signers = null))
    }

    @Test
    fun digestCompareIsCaseInsensitive() {
        assertEquals(ApkCheck.Accept(a), check(signers = listOf(a.uppercase())))
    }

    @Test
    fun packageReasonWinsOverSignerReason() {
        assertTrue(reason(check(pkg = "com.evil.app", signers = listOf(b))).contains("package"))
    }
}
