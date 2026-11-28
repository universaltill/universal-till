package com.universaltill.pos

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * ut-docs#7097 T8: onResume asks the till's cloud-credential self-heal to
 * check now. Activity resumes happen often (every dialog, every permission
 * screen), so repeat calls inside the interval are swallowed. Pure, so it
 * runs on the JVM. Times are SystemClock.elapsedRealtime() values.
 */
class ForegroundThrottleTest {
    private var now = 1_000_000L
    private val throttle = ForegroundThrottle(minIntervalMs = 30_000L) { now }

    @Test
    fun theFirstResumeFires() {
        assertTrue(throttle.shouldFire())
    }

    @Test
    fun aSecondResumeInsideTheIntervalIsSwallowed() {
        assertTrue(throttle.shouldFire())
        now += 29_999
        assertFalse(throttle.shouldFire())
    }

    @Test
    fun itFiresAgainExactlyAtTheInterval() {
        assertTrue(throttle.shouldFire())
        now += 30_000
        assertTrue(throttle.shouldFire())
    }

    @Test
    fun aSwallowedCallDoesNotPushTheWindowOut() {
        assertTrue(throttle.shouldFire())
        now += 20_000
        assertFalse(throttle.shouldFire())
        now += 10_000
        assertTrue(throttle.shouldFire())
    }

    @Test
    fun resetLetsTheNextResumeFireAtOnce() {
        assertTrue(throttle.shouldFire())
        throttle.reset()
        assertTrue(throttle.shouldFire())
    }
}
