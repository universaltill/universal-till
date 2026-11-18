package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class TapToPayCapabilityTest {
    private val ok = readyFacts()

    private fun reason(f: DeviceFacts) = TapToPayCapability.evaluate(f)

    @Test fun readyDeviceHasNoReason() {
        val c = reason(ok)
        assertTrue(c.supported); assertTrue(c.ready); assertNull(c.reason); assertTrue(c.needs.isEmpty())
    }

    @Test fun eachPermanentReasonAlone() {
        val cases = listOf(
            "flag_off" to ok.copy(flagOn = false),
            "sdk_missing" to ok.copy(sdkPresent = false),
            "unsupported_android" to ok.copy(sdkInt = 32),
            "unsupported_cpu" to ok.copy(abiOk = false),
            "no_nfc" to ok.copy(hasNfc = false),
            "no_gms" to ok.copy(hasGms = false),
            "no_gms" to ok.copy(hasPlayStore = false),
            "no_hw_keystore" to ok.copy(keystoreVersion = 41),
            "security_patch_old" to ok.copy(securityPatchEpochDay = ok.nowEpochDay - 395),
            "security_patch_old" to ok.copy(securityPatchEpochDay = null),
            "developer_options_on" to ok.copy(devOptionsOn = true),
            "debuggable_build" to ok.copy(debuggable = true),
        )
        for ((want, f) in cases) {
            val c = reason(f)
            assertEquals(want, c.reason)
            assertFalse("$want must hide the entry", c.supported)
            assertFalse(c.ready)
        }
    }

    @Test fun android13Passes12Fails() {
        assertEquals("unsupported_android", reason(ok.copy(sdkInt = 32)).reason)
        assertNull(reason(ok.copy(sdkInt = 33)).reason)
    }

    @Test fun patchWithinTwelveMonthsPasses() {
        assertNull(reason(ok.copy(securityPatchEpochDay = ok.nowEpochDay - 360)).reason)
    }

    @Test fun flagOffAlwaysWins() {
        val worst = ok.copy(flagOn = false, sdkPresent = false, sdkInt = 28, hasNfc = false, hasGms = false)
        assertEquals("flag_off", reason(worst).reason)
    }

    @Test fun orderIsFirstFailingCheck() {
        assertEquals("sdk_missing", reason(ok.copy(sdkPresent = false, sdkInt = 28)).reason)
        assertEquals("unsupported_android", reason(ok.copy(sdkInt = 28, hasNfc = false)).reason)
        assertEquals("no_nfc", reason(ok.copy(hasNfc = false, hasGms = false)).reason)
    }

    @Test fun fireOsArmv7FactsAreUnsupportedForThreeIndependentReasons() {
        val fire = ok.copy(sdkInt = 28, hasGms = false, hasNfc = false, sdkPresent = false)
        assertFalse(reason(fire).supported)
        assertEquals("unsupported_android", reason(fire.copy(sdkPresent = true)).reason)
        assertEquals("no_gms", reason(ok.copy(hasGms = false)).reason)
    }

    @Test fun fixableReasonsKeepTheEntryVisibleWithANeed() {
        val cases = listOf(
            "nfc_off" to ok.copy(nfcEnabled = false),
            "location_permission" to ok.copy(locationPermission = false),
            "location_off" to ok.copy(locationServicesOn = false),
            "offline" to ok.copy(online = false),
            "education_required" to ok.copy(educationAccepted = false),
        )
        for ((want, f) in cases) {
            val c = reason(f)
            assertEquals(want, c.reason)
            assertTrue(c.supported)
            assertFalse(c.ready)
            assertTrue(c.needs.contains(want))
        }
    }

    @Test fun needsListsEveryFixableStep() {
        val c = reason(ok.copy(nfcEnabled = false, locationServicesOn = false, educationAccepted = false))
        assertEquals(listOf("nfc_off", "location_off", "education_required"), c.needs)
    }

    @Test fun readyImpliesNoReason() {
        val variants = listOf(ok, ok.copy(online = false), ok.copy(hasNfc = false), ok.copy(educationAccepted = false))
        for (f in variants) {
            val c = reason(f)
            if (c.ready) assertNull(c.reason) else assertTrue(c.reason != null)
        }
    }
}
