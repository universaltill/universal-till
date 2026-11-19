package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class GateAndBridgeTest {
    private fun engine(): Pair<TapToPayEngine, FakeReader> {
        val sched = FakeScheduler()
        val reader = FakeReader(sched)
        val e = TapToPayEngine(FakeProbe(readyFacts()), reader, sched, MemoryPendingStore(), MemoryDoneStore(), FakeUi())
        e.onForeground()
        return e to reader
    }

    @Test fun flagOffNeverBuildsAnEngine() {
        var built = 0
        assertNull(TapToPayGate.engineOrNull(flagOn = false, sdkCompiledIn = true) { built++; engine().first })
        assertNull(TapToPayGate.engineOrNull(flagOn = false, sdkCompiledIn = false) { built++; engine().first })
        assertEquals("the factory must not even run", 0, built)
    }

    @Test fun flagOnWithoutTheSdkBuildsNothing() {
        var built = 0
        assertNull(TapToPayGate.engineOrNull(flagOn = true, sdkCompiledIn = false) { built++; engine().first })
        assertEquals(0, built)
    }

    @Test fun flagOnWithTheSdkBuildsTheEngine() {
        assertTrue(TapToPayGate.engineOrNull(flagOn = true, sdkCompiledIn = true) { engine().first } != null)
    }

    @Test fun switchableUiWithoutAnActivityRefusesEverything() {
        val ui = SwitchableUi()
        var edu: Boolean? = null
        var perm: Boolean? = null
        ui.showEducation { edu = it }
        ui.requestLocationPermission { perm = it }
        assertEquals(false, edu); assertEquals(false, perm)
        assertFalse(ui.openSettings("nfc"))
        val fake = FakeUi()
        ui.target = fake
        assertTrue(ui.openSettings("nfc"))
        assertEquals(listOf("nfc"), fake.opened)
    }

    @Test fun connectGateRunsOneConnectForConcurrentCallers() {
        val g = FlightGate()
        val got = mutableListOf<String?>()
        assertTrue(g.join { got += it })
        assertFalse(g.join { got += it })
        assertTrue(g.inFlight)
        g.finish("offline")
        assertEquals(listOf<String?>("offline", "offline"), got)
        assertFalse(g.inFlight)
        assertTrue(g.join { got += it }) // a later attempt starts afresh
    }
}
