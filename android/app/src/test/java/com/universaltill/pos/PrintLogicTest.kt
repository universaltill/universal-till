package com.universaltill.pos

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * ut-docs#6153: the pure decisions behind the Android print bridge (job name
 * from the untrusted page title, the one-dialog-at-a-time guard, and when
 * printing is refused). Free of Android types so they run on the JVM.
 */
class PrintLogicTest {
    private val app = "Universal Till"

    @Test
    fun jobNamePrefixesTheAppName() {
        assertEquals("Universal Till – Receipt R-1", PrintLogic.jobName(app, "Receipt R-1"))
    }

    @Test
    fun nullOrBlankTitleIsTheAppNameAlone() {
        assertEquals(app, PrintLogic.jobName(app, null))
        assertEquals(app, PrintLogic.jobName(app, ""))
        assertEquals(app, PrintLogic.jobName(app, "  \n\t "))
    }

    @Test
    fun controlCharactersBecomeSpaces() {
        assertEquals("Universal Till – a b c d", PrintLogic.jobName(app, "a\nb\rc\td"))
        assertEquals("Universal Till – x y", PrintLogic.jobName(app, "x\u0000\u001B\u007Fy"))
    }

    @Test
    fun whitespaceIsCollapsedAndTrimmed() {
        assertEquals("Universal Till – a b", PrintLogic.jobName(app, "  a \n\n   b  "))
    }

    @Test
    fun jobNameIsCappedAtTheMax() {
        val name = PrintLogic.jobName(app, "x".repeat(500))
        assertEquals(PrintLogic.JOB_NAME_MAX, name.codePointCount(0, name.length))
        assertTrue(name.startsWith("Universal Till – xxx"))
    }

    @Test
    fun capNeverSplitsASurrogatePair() {
        val emoji = "😀" // one code point, two chars
        val name = PrintLogic.jobName(app, emoji.repeat(200))
        assertEquals(PrintLogic.JOB_NAME_MAX, name.codePointCount(0, name.length))
        assertFalse(Character.isHighSurrogate(name.last()))
        assertTrue(name.endsWith(emoji))
    }

    @Test
    fun cappedNameHasNoTrailingSpace() {
        // app name + " – " is 17 chars; a space landing on the cut is trimmed.
        val title = "y".repeat(PrintLogic.JOB_NAME_MAX - 18) + " zzzz"
        val name = PrintLogic.jobName(app, title)
        assertEquals(name.trimEnd(), name)
    }

    @Test
    fun zeroMeansNoJobInFlight() {
        assertFalse(PrintLogic.inFlight(0L, 5_000L))
    }

    @Test
    fun aJobStartedJustNowIsInFlight() {
        assertTrue(PrintLogic.inFlight(1_000L, 1_001L))
    }

    @Test
    fun aJobThatNeverFinishedIsNotInFlightAfterTheStaleWindow() {
        val since = 1_000L
        assertTrue(PrintLogic.inFlight(since, since + PrintLogic.IN_FLIGHT_STALE_MS - 1))
        assertFalse(PrintLogic.inFlight(since, since + PrintLogic.IN_FLIGHT_STALE_MS))
    }

    @Test
    fun printIsRefusedWhenPinned() {
        assertFalse(PrintLogic.canPrint(pinned = true, activityGone = false, inFlightSinceMs = 0L, nowMs = 10L))
    }

    @Test
    fun printIsRefusedWhenTheActivityIsGone() {
        assertFalse(PrintLogic.canPrint(pinned = false, activityGone = true, inFlightSinceMs = 0L, nowMs = 10L))
    }

    @Test
    fun printIsRefusedWhileAJobIsInFlight() {
        assertFalse(PrintLogic.canPrint(pinned = false, activityGone = false, inFlightSinceMs = 5L, nowMs = 10L))
    }

    @Test
    fun printIsAllowedOtherwise() {
        assertTrue(PrintLogic.canPrint(pinned = false, activityGone = false, inFlightSinceMs = 0L, nowMs = 10L))
        assertTrue(
            PrintLogic.canPrint(
                pinned = false, activityGone = false,
                inFlightSinceMs = 5L, nowMs = 5L + PrintLogic.IN_FLIGHT_STALE_MS,
            ),
        )
    }
}
