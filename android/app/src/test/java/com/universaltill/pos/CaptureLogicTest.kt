package com.universaltill.pos

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * ut-docs#5190: when a page's file input asks for the camera (the item photo's
 * `capture="environment"` input), the app opens the system camera instead of
 * the document picker. The decision is pure so it runs on the JVM.
 */
class CaptureLogicTest {
    @Test
    fun captureInputForImagesOpensTheCamera() {
        assertTrue(CaptureLogic.wantsCamera(true, listOf("image/*")))
        assertTrue(CaptureLogic.wantsCamera(true, listOf("image/jpeg", "image/png")))
        assertTrue(CaptureLogic.wantsCamera(true, emptyList()))
        assertTrue(CaptureLogic.wantsCamera(true, listOf("")))
    }

    @Test
    fun anInputWithoutCaptureStaysTheDocumentPicker() {
        assertFalse(CaptureLogic.wantsCamera(false, listOf("image/*")))
        assertFalse(CaptureLogic.wantsCamera(false, emptyList()))
    }

    @Test
    fun captureForNonImageFilesIsNotACameraRequest() {
        // e.g. a backup restore or plugin file input must never open the camera
        assertFalse(CaptureLogic.wantsCamera(true, listOf(".db")))
        assertFalse(CaptureLogic.wantsCamera(true, listOf("application/zip")))
        assertFalse(CaptureLogic.wantsCamera(true, listOf("video/*")))
    }

    @Test
    fun captureFilesAreNamedPerShotAndOldOnesAreSwept() {
        assertEquals("photo-1000.jpg", CaptureLogic.captureFileName(1000))
        val names = listOf("photo-1000.jpg", "photo-9000.jpg", "notes.txt")
        // anything older than the max age goes; foreign files are never touched
        assertEquals(listOf("photo-1000.jpg"), CaptureLogic.staleCaptures(names, 10_000, 5_000))
        assertEquals(emptyList<String>(), CaptureLogic.staleCaptures(names, 10_000, 20_000))
    }
}
