package com.universaltill.pos.tap

import com.universaltill.pos.BuildConfig
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * #3048: the flag is compile-time and only the arm64 flavor may honour it.
 * UT_EXPECT_TAP_TO_PAY (set by android-ci for the -PtapToPay=true run) is the
 * expected arm64 value; every other flavor must always be false.
 */
class TapToPayBuildConfigTest {
    private val expectOn = System.getenv("UT_EXPECT_TAP_TO_PAY") == "true"

    @Test fun onlyArm64MayHaveItOn() {
        if (BuildConfig.FLAVOR == "arm64") {
            assertTrue(BuildConfig.TAP_TO_PAY_ENABLED == expectOn)
        } else {
            assertFalse("${BuildConfig.FLAVOR} must never enable Tap to Pay", BuildConfig.TAP_TO_PAY_ENABLED)
        }
    }

    @Test fun platformMatchesTheFlag() {
        assertTrue(TapToPayPlatform.sdkCompiledIn == BuildConfig.TAP_TO_PAY_ENABLED)
    }
}
