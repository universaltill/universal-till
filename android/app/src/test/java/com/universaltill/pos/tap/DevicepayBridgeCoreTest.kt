package com.universaltill.pos.tap

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class DevicepayBridgeCoreTest {
    private lateinit var sched: FakeScheduler
    private lateinit var reader: FakeReader
    private lateinit var store: MemoryPendingStore
    private lateinit var done: MemoryDoneStore
    private lateinit var probe: FakeProbe
    private lateinit var engine: TapToPayEngine
    private lateinit var core: DevicepayBridgeCore
    private val wall = 1_700_000_000_000L
    private var posts = 0

    @Before fun setUp() {
        sched = FakeScheduler(); reader = FakeReader(sched); store = MemoryPendingStore(); done = MemoryDoneStore()
        probe = FakeProbe(readyFacts())
        engine = TapToPayEngine(probe, reader, sched, store, done, FakeUi())
        engine.onForeground()
        // The main thread, run inline: the fakes complete synchronously.
        core = DevicepayBridgeCore(engine, { posts++; it() }, { wall }, backstopMs = 30, setupBackstopMs = 30)
    }

    private fun request(amount: Long = 1250, key: String = "sale-1", deadline: Long = wall + 60_000, provider: String = "stripe") =
        """{"provider":"$provider","amount_minor":$amount,"currency":"GBP","idempotency_key":"$key","description":"Sale","deadline_unix_ms":$deadline}"""

    private fun collect(raw: String?) = JSONObject(core.collect(raw))

    @Test fun theAmountCurrencyAndKeyReachTheReaderFromGoesRequestOnly() {
        val o = collect(request())
        assertEquals("approved", o.getString("status")); assertEquals("pi_1", o.getString("provider_txn_id"))
        assertEquals(listOf<Any>(1250L, "gbp", "sale-1"), reader.createCalls.single())
    }

    @Test fun anUnreadableRequestIsDeclinedAndNothingStarts() {
        for (bad in listOf(null, "x", request(amount = 0), request(provider = "sumup"))) {
            val o = collect(bad)
            assertEquals("declined", o.getString("status")); assertEquals("bad_request", o.getString("reason"))
        }
        assertTrue(reader.createCalls.isEmpty())
        assertEquals(0, posts)
    }

    @Test fun anExpiredDeadlineIsPendingAndNeverStartsACollect() {
        val o = collect(request(deadline = wall - 1))
        assertEquals("pending", o.getString("status")); assertEquals("timeout", o.getString("reason"))
        assertTrue(reader.createCalls.isEmpty())
    }

    @Test fun aRefusedStartIsDeclinedWithItsReason() {
        probe.facts = readyFacts().copy(online = false)
        val o = collect(request())
        assertEquals("declined", o.getString("status")); assertEquals("offline", o.getString("reason"))
    }

    @Test fun aDeclineAndAnUnknownAreDistinct() {
        reader.autoConfirm = ConfirmOutcome.Declined("insufficient_funds")
        val d = collect(request(key = "k1"))
        assertEquals("declined", d.getString("status")); assertEquals("insufficient_funds", d.getString("decline_code"))
        reader.autoConfirm = ConfirmOutcome.Failed("network")
        val u = collect(request(key = "k1")) // same key: the live intent
        assertEquals("pending", u.getString("status"))
    }

    @Test fun aVerdictThatNeverComesIsPendingAfterTheDeadlineAndTheCollectIsCancelled() {
        reader.autoCollect = null
        val o = collect(request(deadline = wall + 40)) // 40 ms + 30 ms backstop of real time
        assertEquals("pending", o.getString("status")); assertEquals("timeout", o.getString("reason"))
        assertEquals("the leftover collect is cancelled", 1, reader.collectCancels)
    }

    @Test fun repeatingAPaidKeyIsApprovedAgainWithNoSecondCharge() {
        collect(request(key = "k9"))
        val again = collect(request(key = "k9"))
        assertEquals("approved", again.getString("status"))
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun cancelReachesTheCollectOfThatKeyOnly() {
        reader.autoCollect = null
        // run the collect on a worker so cancel can arrive meanwhile; the inline executor is fine for cancel itself
        val t = Thread { collect(request(key = "kc", deadline = wall + 5_000)) }
        t.start()
        while (reader.collectCb == null) Thread.sleep(1)
        core.cancel("someone-else")
        assertEquals(0, reader.collectCancels)
        core.cancel("kc")
        t.join(2_000)
        assertEquals(1, reader.collectCancels)
    }

    @Test fun setupAndEducationOnlyForStripe() {
        assertEquals("unsupported_provider", JSONObject(core.setup("sumup")).getString("reason"))
        assertEquals("unsupported_provider", JSONObject(core.showEducation(null)).getString("reason"))
        assertTrue(JSONObject(core.setup("stripe")).getBoolean("ok"))
        assertTrue(JSONObject(core.showEducation("stripe")).getBoolean("ok"))
    }

    @Test fun aDeclinedEducationIsReported() {
        val ui = FakeUi().also { it.educationAnswer = false }
        val c = DevicepayBridgeCore(TapToPayEngine(probe, reader, sched, store, done, ui), { it() }, { wall })
        val o = JSONObject(c.showEducation("stripe"))
        assertFalse(o.getBoolean("ok")); assertEquals("education_declined", o.getString("reason"))
    }

    @Test fun setupThatNeverAnswersTimesOut() {
        probe.facts = readyFacts().copy(locationPermission = false)
        val ui = object : UiActions by FakeUi() {
            override fun requestLocationPermission(cb: (Boolean) -> Unit) { /* the person walked away */ }
        }
        val c = DevicepayBridgeCore(TapToPayEngine(probe, reader, sched, store, done, ui), { it() }, { wall }, setupBackstopMs = 20)
        assertEquals("timeout", JSONObject(c.setup("stripe")).getString("reason"))
    }

    @Test fun resolveNotChargedClearsTheRecordAndUnblocksOtherKeys() {
        store.put(PendingRecord("old", "k1", "pi_77", "unknown", 500L, "gbp"))
        assertEquals("unknown_pending", collect(request(key = "k2")).getString("reason"))
        assertTrue(JSONObject(core.resolve("k1", "not_charged")).getBoolean("ok"))
        assertEquals("approved", collect(request(key = "k2")).getString("status"))
        val gone = JSONObject(core.resolve("k1", "not_charged"))
        assertFalse(gone.getBoolean("ok")); assertEquals("no_such_payment", gone.getString("reason"))
        assertEquals("bad_request", JSONObject(core.resolve("", "not_charged")).getString("reason"))
        assertEquals("bad_request", JSONObject(core.resolve(null, "not_charged")).getString("reason"))
    }

    @Test fun resolveChargedIsAcceptedAndMarksTheKeyPaid() {
        store.put(PendingRecord("old", "k1", "pi_77", "unknown", 500L, "gbp"))
        assertTrue(JSONObject(core.resolve("k1", "charged")).getBoolean("ok"))
        assertEquals("pi_77", done.load("k1")?.intentId)
        assertNull(store.load("k1"))
    }

    @Test fun resolveWithAnUnknownOrMissingOutcomeClearsNothing() {
        store.put(PendingRecord("old", "k1", "pi_77", "unknown", 500L, "gbp"))
        for (bad in listOf(null, "", "refund", "Charged")) {
            assertEquals("bad_request", JSONObject(core.resolve("k1", bad)).getString("reason"))
        }
        assertEquals("unknown", store.load("k1")?.state)
        assertNull(done.load("k1"))
    }

    // The role gate lives in Go (devicepay.Resolve refuses a non-manager before the native side; see the Go tests
    // TestResolve_UnknownOutcomeAndCashierNeverReachNative and mobile TestSetTapToPayResolver_*). This class has no role.
    @Test fun theBridgeMirrorHasNoResolveUnknownLeftToCallAroundTheOutcomeCheck() {
        assertTrue(GoDevicepayBridge::class.java.methods.none { it.name == "resolveUnknown" })
        assertTrue(GoDevicepayBridge::class.java.methods.any { it.name == "resolve" })
    }

    @Test fun sameKeyAfterAnUnresolvedRecordIsPendingNotDeclined() {
        store.put(PendingRecord("old", "k1", "pi_77", "confirming", 500L, "gbp"))
        val o = collect(request(key = "k1"))
        assertEquals("pending", o.getString("status")); assertEquals("outcome_unknown", o.getString("reason"))
    }

    @Test fun capabilitiesNeverTouchTheMainThread() {
        val before = posts
        val o = JSONObject(core.capabilities()).getJSONObject("tap_to_pay").getJSONObject("stripe")
        assertTrue(o.getBoolean("ready"))
        assertEquals(before, posts)
    }
}
