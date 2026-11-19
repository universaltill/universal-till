package com.universaltill.pos

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.fail
import org.junit.Test

/**
 * ut-docs#6551: the pure decisions behind the "allow installs from this app"
 * pre-flight, kept free of Android types so they run on the JVM.
 */
class InstallPermissionLogicTest {
    @Test
    fun allowedProceedsToTheInstaller() {
        assertEquals(InstallPreflight.PROCEED, UpdateLogic.installPreflight(26) { true })
    }

    @Test
    fun notAllowedAsksForPermissionInsteadOfOpeningTheInstaller() {
        assertEquals(InstallPreflight.NEEDS_PERMISSION, UpdateLogic.installPreflight(26) { false })
    }

    @Test
    fun resumeWhenNotWaitingReportsNothing() {
        assertNull(UpdateLogic.permissionResume(awaiting = false, sdkInt = 26) { true })
        assertNull(UpdateLogic.permissionResume(awaiting = false, sdkInt = 26) { false })
    }

    @Test
    fun resumeAfterGrantingReportsGranted() {
        assertEquals(PermissionResume.GRANTED, UpdateLogic.permissionResume(awaiting = true, sdkInt = 26) { true })
    }

    @Test
    fun resumeStillDeniedSaysSo() {
        assertEquals(PermissionResume.STILL_DENIED, UpdateLogic.permissionResume(awaiting = true, sdkInt = 26) { false })
    }

    @Test
    fun permissionStatesUseTheWireNamesThePageListensFor() {
        assertEquals("needs_permission", UpdateLogic.STATE_NEEDS_PERMISSION)
        assertEquals("permission_granted", UpdateLogic.STATE_PERMISSION_GRANTED)
        assertEquals(
            """{"state":"needs_permission","percent":null,"error":"still_denied"}""",
            UpdateLogic.stateJson(UpdateLogic.STATE_NEEDS_PERMISSION, null, UpdateLogic.ERROR_STILL_DENIED),
        )
    }

    @Test
    fun beforeAndroid8ThePermissionApiIsNeverCalledAndInstallProceeds() {
        for (sdk in listOf(24, 25)) {
            assertEquals(
                InstallPreflight.PROCEED,
                UpdateLogic.installPreflight(sdk) { fail("canRequestPackageInstalls needs API 26"); false },
            )
        }
    }

    @Test
    fun beforeAndroid8ResumeReportsNothingAndNeverCallsTheApi() {
        for (sdk in listOf(24, 25)) {
            assertNull(
                UpdateLogic.permissionResume(awaiting = true, sdkInt = sdk) { fail("needs API 26"); false },
            )
        }
    }

    @Test
    fun resumeNotAwaitingNeverCallsTheApi() {
        assertNull(UpdateLogic.permissionResume(awaiting = false, sdkInt = 34) { fail("not awaiting"); false })
    }
}
