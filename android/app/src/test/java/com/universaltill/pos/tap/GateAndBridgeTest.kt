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
        val e = TapToPayEngine(FakeProbe(readyFacts()), reader, sched, MemoryPendingStore(), MemoryDoneStore(), FakeUi()) { }
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

    @Test fun listenerRuleSetIsTheExactOriginOnly() {
        assertEquals(setOf("http://127.0.0.1:8080"), BridgeCore.originRules("127.0.0.1:8080"))
    }

    @Test fun messageFromAnotherOriginOrFrameIsDroppedSilently() {
        val (e, reader) = engine()
        val out = SwitchableOut()
        val core = BridgeCore(e, out, "127.0.0.1:8080")
        val replies = mutableListOf<String>()
        val start = """{"v":1,"id":"r1","op":"start","amount_minor":500,"currency":"gbp","idempotency_key":"k"}"""
        core.onMessage(start, "http://evil.example", true) { replies += it }
        core.onMessage(start, "http://127.0.0.1:8080", false) { replies += it }
        core.onMessage(start, "http://127.0.0.1:9999", true) { replies += it }
        core.onMessage(null, "http://127.0.0.1:8080", true) { replies += it }
        assertTrue(replies.isEmpty())
        assertTrue(reader.createCalls.isEmpty())
        assertNull(out.proxy)
    }

    @Test fun messageFromTheTillMainFrameReachesTheEngineAndRemembersTheReplyChannel() {
        val (e, reader) = engine()
        val out = SwitchableOut()
        val core = BridgeCore(e, out, "127.0.0.1:8080")
        val replies = mutableListOf<String>()
        core.onMessage("""{"v":1,"id":"r1","op":"start","amount_minor":500,"currency":"gbp","idempotency_key":"k"}""", "http://127.0.0.1:8080", true) { replies += it }
        assertEquals(1, replies.size)
        assertEquals(1, reader.createCalls.size)
        assertTrue(out.proxy != null)
    }

    @Test fun switchableOutSwallowsAStaleChannel() {
        val out = SwitchableOut()
        out.proxy = { throw IllegalStateException("page reloaded") }
        out.emit("{}") // must not throw
        out.proxy = null
        out.emit("{}")
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
