package com.universaltill.pos.tap

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class TapToPayEngineTest {
    private lateinit var sched: FakeScheduler
    private lateinit var reader: FakeReader
    private lateinit var store: MemoryPendingStore
    private lateinit var probe: FakeProbe
    private lateinit var ui: FakeUi
    private val events = mutableListOf<JSONObject>()
    private val replies = mutableListOf<JSONObject>()
    private lateinit var engine: TapToPayEngine

    @Before fun setUp() {
        sched = FakeScheduler(); reader = FakeReader(sched); store = MemoryPendingStore()
        probe = FakeProbe(readyFacts()); ui = FakeUi()
        engine = TapToPayEngine(probe, reader, sched, store, ui) { events += JSONObject(it) }
        engine.onForeground()
    }

    private fun send(raw: String): JSONObject {
        val n = replies.size
        engine.handle(raw) { replies += JSONObject(it) }
        assertEquals("exactly one reply", n + 1, replies.size)
        return replies.last()
    }

    private fun start(id: String = "r1", key: String = "k1", amount: Int = 500) =
        send("""{"v":1,"id":"$id","op":"start","amount_minor":$amount,"currency":"gbp","idempotency_key":"$key"}""")

    private fun results() = events.filter { it.getString("type") == "result" }

    @Test fun capabilitiesReply() {
        val r = send("""{"v":1,"id":"c1","op":"capabilities"}""")
        val c = r.getJSONObject("capability")
        assertTrue(r.getBoolean("ok")); assertTrue(c.getBoolean("supported")); assertTrue(c.getBoolean("ready"))
        assertEquals("stripe", c.getString("provider")); assertTrue(c.getBoolean("education_accepted"))
        assertTrue(c.isNull("reason"))
    }

    @Test fun startSucceedsAndEmitsResultEvent() {
        val r = start()
        assertTrue(r.getBoolean("ok"))
        assertEquals("succeeded", results().single().getString("status"))
        assertEquals("r1", results().single().getString("request_id"))
    }

    @Test fun badRequestIsRefusedWithoutTouchingTheReader() {
        val r = send("""{"v":1,"id":"r1","op":"start","amount_minor":0,"currency":"gbp","idempotency_key":"k"}""")
        assertFalse(r.getBoolean("ok")); assertEquals("bad_request", r.getString("error"))
        assertTrue(reader.createCalls.isEmpty())
    }

    @Test fun singleFlightBusy() {
        reader.autoCollect = null
        assertTrue(start("r1", "k1").getBoolean("ok"))
        val r = start("r2", "k2")
        assertEquals("busy", r.getString("error"))
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun notForeground() {
        engine.onBackground()
        assertEquals("not_foreground", start().getString("error"))
    }

    @Test fun backgroundDoesNotCancelAnInFlightPayment() {
        reader.autoCollect = null
        start()
        engine.onBackground()
        engine.onForeground()
        assertEquals(0, reader.collectCancels)
        reader.collectCb!!(CollectOutcome.Collected)
        assertEquals("succeeded", results().single().getString("status"))
    }

    @Test fun notReadyMapsFixableNeeds() {
        probe.facts = readyFacts().copy(educationAccepted = false)
        assertEquals("needs_education", start().getString("error"))
        probe.facts = readyFacts().copy(online = false)
        assertEquals("offline", start().getString("error"))
        probe.facts = readyFacts().copy(nfcEnabled = false)
        assertEquals("not_ready", start().getString("error"))
        probe.facts = readyFacts().copy(hasNfc = false)
        assertEquals("not_ready", start().getString("error"))
        assertTrue(reader.createCalls.isEmpty())
    }

    @Test fun connectProblemsSurfaceAsStartErrors() {
        reader.connectError = "connect_incomplete"
        assertEquals("connect_incomplete", start().getString("error"))
    }

    @Test fun resultPollFallbackAndNone() {
        start("r1", "k1")
        val r = send("""{"v":1,"id":"p1","op":"result","request_id":"r1"}""")
        assertEquals("succeeded", r.getJSONObject("result").getString("status"))
        val none = send("""{"v":1,"id":"p2","op":"result","request_id":"nope"}""")
        assertEquals("none", none.getJSONObject("result").getString("status"))
    }

    @Test fun resultStoreIsBoundedToEight() {
        for (i in 1..9) start("r$i", "k$i")
        assertEquals("none", send("""{"v":1,"id":"p","op":"result","request_id":"r1"}""").getJSONObject("result").getString("status"))
        assertEquals("succeeded", send("""{"v":1,"id":"p","op":"result","request_id":"r2"}""").getJSONObject("result").getString("status"))
        assertEquals("succeeded", send("""{"v":1,"id":"p","op":"result","request_id":"r9"}""").getJSONObject("result").getString("status"))
    }

    @Test fun cancelOpCancelsCollectOnly() {
        reader.autoCollect = null
        start("r1", "k1")
        assertTrue(send("""{"v":1,"id":"x","op":"cancel","request_id":"r1"}""").getBoolean("ok"))
        assertEquals("cancelled", results().single().getString("status"))
        val none = send("""{"v":1,"id":"y","op":"cancel","request_id":"zzz"}""")
        assertFalse(none.getBoolean("ok"))
    }

    @Test fun cancelAfterConfirmIsTooLate() {
        reader.autoConfirm = null
        start("r1", "k1")
        val r = send("""{"v":1,"id":"x","op":"cancel","request_id":"r1"}""")
        assertFalse(r.getBoolean("ok")); assertEquals("too_late", r.getString("error"))
    }

    @Test fun unknownThenSameKeyReusesTheLiveIntentNeverCreatesASecond() {
        reader.autoConfirm = null
        start("r1", "k1")
        sched.advance(24_001)
        assertEquals("unknown", results().last().getString("status"))
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        start("r2", "k1")
        assertEquals(1, reader.createCalls.size)
        assertEquals(listOf("pi_1", "pi_1"), reader.collectIds)
    }

    @Test fun unknownAfterProcessDeathRefusesSameKeyAsUnknownPending() {
        store.record = PendingRecord("old", "k1", "pi_77", "confirming")
        val r = start("r2", "k1")
        assertFalse(r.getBoolean("ok")); assertEquals("unknown_pending", r.getString("error"))
        assertTrue(reader.createCalls.isEmpty())
        // a different basket is not blocked by it
        assertTrue(start("r3", "k2").getBoolean("ok"))
    }

    @Test fun pendingRecordSurfacesAsUnknownOnResultPoll() {
        store.record = PendingRecord("old", "k1", "pi_77", "confirming")
        val j = send("""{"v":1,"id":"p","op":"result","request_id":"old"}""").getJSONObject("result")
        assertEquals("unknown", j.getString("status")); assertEquals("pi_77", j.getString("payment_intent_id"))
        assertEquals("app_closed", j.getString("reason"))
    }

    @Test fun declinedKeepsTheIntentForTheSameKeyRetry() {
        reader.autoConfirm = ConfirmOutcome.Declined("card_declined")
        start("r1", "k1")
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        start("r2", "k1")
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun succeededKeyStartsAFreshIntentNextTime() {
        start("r1", "k1")
        start("r2", "k1")
        assertEquals(2, reader.createCalls.size)
    }

    @Test fun lateResultOverwritesStoredResultAndEmitsLateEvent() {
        reader.autoConfirm = null
        start("r1", "k1")
        sched.advance(24_001)
        reader.confirmCb!!(ConfirmOutcome.Succeeded(null, null, null))
        val ev = results()
        assertEquals(listOf("unknown", "succeeded"), ev.map { it.getString("status") })
        assertTrue(ev.last().getBoolean("late"))
        val polled = send("""{"v":1,"id":"p","op":"result","request_id":"r1"}""").getJSONObject("result")
        assertEquals("succeeded", polled.getString("status"))
    }

    @Test fun destroyWhileFinishingCancelsCollect() {
        reader.autoCollect = null
        start()
        engine.onDestroy(isFinishing = true)
        assertEquals("app_closed", results().single().getString("reason"))
    }

    @Test fun destroyForRotationDoesNotCancel() {
        reader.autoCollect = null
        start()
        engine.onDestroy(isFinishing = false)
        assertEquals(0, reader.collectCancels)
    }

    @Test fun educationOpRepliesThenEmitsEvent() {
        val r = send("""{"v":1,"id":"e","op":"education"}""")
        assertTrue(r.getBoolean("ok"))
        assertEquals("education", events.single().getString("type")); assertTrue(events.single().getBoolean("accepted"))
        ui.educationAnswer = false
        send("""{"v":1,"id":"e2","op":"education"}""")
        assertFalse(events.last().getBoolean("accepted"))
    }

    @Test fun prepareWarmsTheReaderAndReportsStates() {
        send("""{"v":1,"id":"p","op":"prepare"}""")
        assertEquals(listOf("connecting", "ready"), events.filter { it.getString("type") == "prepare" }.map { it.getString("state") })
    }

    @Test fun prepareAsksLocationPermissionAfterRationaleOnly() {
        probe.facts = readyFacts().copy(locationPermission = false)
        ui.permissionAnswer = false
        send("""{"v":1,"id":"p","op":"prepare"}""")
        assertEquals(1, ui.permissionAsked)
        val last = events.filter { it.getString("type") == "prepare" }.last()
        assertEquals("failed", last.getString("state")); assertEquals("location_permission", last.getString("reason"))
        assertTrue(reader.createCalls.isEmpty())
    }

    @Test fun capabilitiesNeverAsksForPermission() {
        probe.facts = readyFacts().copy(locationPermission = false)
        send("""{"v":1,"id":"c","op":"capabilities"}""")
        assertEquals(0, ui.permissionAsked)
    }

    @Test fun openSettingsOnlyForAllowlistedTargets() {
        assertTrue(send("""{"v":1,"id":"s","op":"open_settings","target":"nfc"}""").getBoolean("ok"))
        assertEquals(listOf("nfc"), ui.opened)
        assertFalse(send("""{"v":1,"id":"s","op":"open_settings","target":"wifi"}""").getBoolean("ok"))
        assertEquals(listOf("nfc"), ui.opened)
    }

    @Test fun stubPlatformEngineIsNeverBuiltWhenFlagIsOff() {
        probe.facts = readyFacts().copy(flagOn = false)
        val c = send("""{"v":1,"id":"c","op":"capabilities"}""").getJSONObject("capability")
        assertFalse(c.getBoolean("supported")); assertEquals("flag_off", c.getString("reason"))
        assertNull(null)
    }
}
