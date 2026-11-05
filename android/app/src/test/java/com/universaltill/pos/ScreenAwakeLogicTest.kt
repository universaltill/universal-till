package com.universaltill.pos

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * ut-docs#5620: the till screen used to follow the OS display timeout (a few
 * seconds on the test tablet) because nothing set FLAG_KEEP_SCREEN_ON. The
 * decision of when the flag is on is pure so it runs on the JVM.
 */
class ScreenAwakeLogicTest {
    private val min = 60_000L

    @Test
    fun theDefaultIsALongTimeoutNotTheOsOne() {
        assertEquals(120, ScreenAwakeLogic.DEFAULT_MINUTES)
        assertEquals(120, ScreenAwakeLogic.normalize(-5))
        assertEquals(120, ScreenAwakeLogic.normalize(7))
    }

    @Test
    fun onlyTheOfferedChoicesAreAccepted() {
        for (m in listOf(0, 15, 30, 60, 120, 240, 480)) {
            assertEquals(m, ScreenAwakeLogic.normalize(m))
        }
    }

    @Test
    fun zeroMeansNeverTimeOutWhileTheAppIsInFront() {
        assertTrue(ScreenAwakeLogic.keepAwake(0, 0, 100 * 60 * min))
        assertNull(ScreenAwakeLogic.remainingMs(0, 0, 100 * 60 * min))
    }

    @Test
    fun stayAwakeUntilTheIdleTimeoutThenLetTheOsSleep() {
        assertTrue(ScreenAwakeLogic.keepAwake(30, 1_000, 1_000 + 29 * min))
        assertFalse(ScreenAwakeLogic.keepAwake(30, 1_000, 1_000 + 30 * min))
        assertEquals(10 * min, ScreenAwakeLogic.remainingMs(30, 0, 20 * min))
    }

    @Test
    fun nothingRemainsOnceTheTimeoutHasPassed() {
        assertEquals(0L, ScreenAwakeLogic.remainingMs(15, 0, 99 * min))
    }
}
