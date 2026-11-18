package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class EpochDayTest {
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
}
