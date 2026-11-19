package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class PendingBoundTest {
    private fun rec(key: String, state: String) = PendingRecord("r-$key", key, "pi-$key", state, 100L, "gbp")

    @Test fun underTheBoundNothingIsDropped() {
        val l = listOf(rec("a", PendingState.UNKNOWN), rec("b", PendingState.CREATED))
        assertEquals(l, PendingBound.keep(l, 4))
    }

    @Test fun anUnresolvedRecordIsNeverEvictedWhileAResolvedOneCanGo() {
        // Review #3048 R4: the oldest record is the unresolved one; a plain takeLast would drop it.
        val l = listOf(rec("old", PendingState.UNKNOWN)) + (1..4).map { rec("c$it", PendingState.CREATED) }
        val kept = PendingBound.keep(l, 4)
        assertEquals(4, kept.size)
        assertTrue(kept.any { it.key == "old" })
        assertEquals(listOf("old", "c2", "c3", "c4"), kept.map { it.key })
    }

    @Test fun onlyWhenEverythingIsUnresolvedDoesTheOldestGo() {
        val l = (1..5).map { rec("u$it", PendingState.CONFIRMING) }
        assertEquals(listOf("u2", "u3", "u4", "u5"), PendingBound.keep(l, 4).map { it.key })
    }
}
