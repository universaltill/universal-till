package com.universaltill.pos.reader

import com.universaltill.pos.tap.TapToPayCapability
import com.universaltill.pos.tap.readyFacts
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ReaderGateTest {
    private fun gate(flag: Boolean = true, f: com.universaltill.pos.tap.DeviceFacts = readyFacts()) =
        ReaderGate.evaluate(flag, TapToPayCapability.evaluate(f))

    @Test fun `reader mode is dark unless its own flag is on`() {
        assertEquals(ReaderGate.Off, gate(flag = false))
        assertEquals(ReaderGate.Available, gate())
    }

    @Test fun `no NFC hides it with the reason`() {
        val g = gate(f = readyFacts().copy(hasNfc = false))
        assertTrue(g is ReaderGate.Unsupported)
    }

    @Test fun `no Play services hides it`() {
        assertTrue(gate(f = readyFacts().copy(hasGms = false)) is ReaderGate.Unsupported)
    }

    @Test fun `Android below 13 or a build without the Stripe SDK hides it`() {
        assertTrue(gate(f = readyFacts().copy(sdkInt = 31)) is ReaderGate.Unsupported)
        assertTrue(gate(f = readyFacts().copy(sdkPresent = false)) is ReaderGate.Unsupported)
        assertTrue(gate(f = readyFacts().copy(flagOn = false)) is ReaderGate.Unsupported)
    }

    @Test fun `a phone that only needs NFC switched on or a permission is still available, setup handles it`() {
        assertEquals(ReaderGate.Available, gate(f = readyFacts().copy(nfcEnabled = false)))
        assertEquals(ReaderGate.Available, gate(f = readyFacts().copy(locationPermission = false)))
    }

    @Test fun `no screen text says charged or not charged`() {
        val xml = listOf("src/main/res/values/strings.xml", "app/src/main/res/values/strings.xml").map { File(it) }.first { it.exists() }.readText()
        val readerStrings = Regex("""<string name="reader_[a-z0-9_]+"[^>]*>([^<]*)</string>""").findAll(xml).map { it.groupValues[1] }.toList()
        assertTrue("no reader strings", readerStrings.size >= 15)
        for (s in readerStrings) {
            val l = s.lowercase()
            assertFalse("'$s'", Regex("""\b(not charged|charged|declined)\b""").containsMatchIn(l))
        }
    }
}
