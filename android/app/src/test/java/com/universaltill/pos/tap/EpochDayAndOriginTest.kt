package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class EpochDayAndOriginTest {
    @Test fun epochDayKnownValues() {
        assertEquals(0L, EpochDay.parse("1970-01-01"))
        assertEquals(10957L, EpochDay.parse("2000-01-01"))
        assertEquals(19723L, EpochDay.parse("2024-01-01"))
        assertEquals(1L, EpochDay.parse("1970-01-02"))
    }

    @Test fun epochDayRejectsGarbage() {
        assertNull(EpochDay.parse(null)); assertNull(EpochDay.parse("")); assertNull(EpochDay.parse("2024-13-01"))
        assertNull(EpochDay.parse("2024-1-1")); assertNull(EpochDay.parse("nope"))
    }

    @Test fun originMustBeTheTillsOwnMainFrame() {
        assertTrue(OriginPolicy.allows("http://127.0.0.1:8080", true, "127.0.0.1:8080"))
        assertFalse(OriginPolicy.allows("http://127.0.0.1:8080", false, "127.0.0.1:8080")) // iframe
        assertFalse(OriginPolicy.allows("http://127.0.0.1:9999", true, "127.0.0.1:8080"))
        assertFalse(OriginPolicy.allows("https://127.0.0.1:8080", true, "127.0.0.1:8080"))
        assertFalse(OriginPolicy.allows("http://evil.example", true, "127.0.0.1:8080"))
        assertFalse(OriginPolicy.allows("null", true, "127.0.0.1:8080"))
        assertFalse(OriginPolicy.allows("http://127.0.0.1:8080", true, null))
    }
}
