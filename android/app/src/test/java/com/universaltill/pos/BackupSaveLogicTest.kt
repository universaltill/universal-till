package com.universaltill.pos

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.IOException

/**
 * ut-docs#5619: Settings -> Backups -> Save to Downloads goes through the
 * system file picker (Downloads, a USB drive, any provider) instead of the
 * till process writing a folder it cannot reach. The pure decisions run here.
 */
class BackupSaveLogicTest {
    @Test
    fun onlyRealBackupNamesAreAccepted() {
        assertTrue(BackupSaveLogic.isValidName("unitill-pos-20260716-120000.db"))
        assertFalse(BackupSaveLogic.isValidName("../unitill-pos-1.db"))
        assertFalse(BackupSaveLogic.isValidName("unitill-pos-1/../../x.db"))
        assertFalse(BackupSaveLogic.isValidName("notes.db"))
        assertFalse(BackupSaveLogic.isValidName("unitill-pos-1.db?x=1"))
        assertFalse(BackupSaveLogic.isValidName(""))
    }

    @Test
    fun downloadUrlIsTheTillsOwnLoopbackOrigin() {
        assertEquals(
            "http://127.0.0.1:8080/api/backup/download/unitill-pos-1.db?via=picker",
            BackupSaveLogic.downloadUrl("127.0.0.1:8080", "unitill-pos-1.db"),
        )
    }

    @Test
    fun failureReasonNamesWhatWentWrong() {
        assertEquals("no_space", BackupSaveLogic.reasonFor(IOException("write failed: ENOSPC (No space left on device)")))
        assertEquals("failed", BackupSaveLogic.reasonFor(IOException("broken pipe")))
        assertEquals("failed", BackupSaveLogic.reasonFor(SecurityException("denied")))
        assertEquals("failed", BackupSaveLogic.reasonFor(null))
    }

    @Test
    fun resultCodesAreTheOnesThePageKnows() {
        assertEquals(setOf("saved", "cancelled", "failed", "no_space"), BackupSaveLogic.RESULT_CODES)
    }
}
