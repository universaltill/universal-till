package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class TapToPayFlowTest {
    private lateinit var sched: FakeScheduler
    private lateinit var reader: FakeReader
    private lateinit var sink: RecordingSink
    private lateinit var store: MemoryPendingStore
    private val req = StartRequest("rq-1", 1250L, "gbp", "ut:sale-1")

    @Before fun setUp() {
        sched = FakeScheduler(); reader = FakeReader(sched); sink = RecordingSink(); store = MemoryPendingStore()
    }

    private fun flow(reuse: String? = null, r: StartRequest = req) = TapToPayFlow(r, reader, sched, sink, store, reuse)

    private fun run(f: TapToPayFlow = flow()): TapToPayFlow {
        f.start { accepted = it; acceptedCalls++ }
        return f
    }

    private var accepted: String? = "unset"
    private var acceptedCalls = 0

    private val last get() = sink.results.last()

    @Test fun success() {
        run()
        assertNull(accepted)
        assertEquals(1, sink.results.size)
        assertEquals(TapStatus.SUCCEEDED, last.status); assertEquals("pi_1", last.paymentIntentId); assertFalse(last.late)
        assertNull(store.only)
        assertEquals(listOf(listOf<Any>(1250L, "gbp", "ut:sale-1")), reader.createCalls)
    }

    @Test fun pendingRecordWrittenBeforeCollect() {
        reader.autoCollect = null
        run()
        assertEquals("created", store.only?.state)
        assertEquals("pi_1", store.only?.intentId)
        assertEquals("ut:sale-1", store.only?.key)
        assertEquals(0, sink.results.size)
    }

    @Test fun declineThenSameKeyRetryReusesTheIntent() {
        reader.autoConfirm = ConfirmOutcome.Declined("insufficient_funds")
        run()
        assertEquals(TapStatus.DECLINED, last.status); assertEquals("insufficient_funds", last.declineCode)
        assertNull(store.only)
        reader.autoConfirm = ConfirmOutcome.Succeeded("ch_9", "visa", "4242")
        run(flow(reuse = last.paymentIntentId))
        assertEquals(1, reader.createCalls.size)
        assertEquals(listOf("pi_1", "pi_1"), reader.collectIds)
        assertEquals(TapStatus.SUCCEEDED, last.status)
        assertEquals("visa", last.cardBrand); assertEquals("4242", last.last4)
    }

    @Test fun customerCancelCancelsTheIntent() {
        reader.autoCollect = CollectOutcome.Cancelled(byCustomer = true)
        run()
        assertEquals(TapStatus.CANCELLED, last.status); assertEquals("customer_cancelled", last.reason)
        assertEquals(listOf("pi_1"), reader.cancelledIntents)
        assertNull(store.only)
    }

    @Test fun operatorCancelDuringCollect() {
        reader.autoCollect = null
        val f = run()
        assertTrue(f.cancel())
        assertEquals(TapStatus.CANCELLED, last.status); assertEquals("operator_cancelled", last.reason)
        assertEquals(1, reader.collectCancels)
        // the SDK's own cancel callback arriving later changes nothing
        reader.collectCb!!(CollectOutcome.Cancelled(false))
        assertEquals(1, sink.results.size)
    }

    @Test fun collectNeverReturnsCancelsAt18s() {
        reader.autoCollect = null
        run()
        sched.advance(17_999)
        assertEquals(0, sink.results.size)
        sched.advance(2)
        assertEquals(TapStatus.CANCELLED, last.status); assertEquals("timeout", last.reason)
        assertEquals(1, reader.collectCancels)
        assertEquals(listOf("pi_1"), reader.cancelledIntents)
    }

    @Test fun intentCancelFailureIsUnknownNotCancelled() {
        reader.autoCollect = null
        reader.cancelIntentOk = false
        run()
        sched.advance(18_001)
        assertEquals(TapStatus.UNKNOWN, last.status)
        assertEquals("pi_1", last.paymentIntentId)
        assertEquals("pi_1", store.only?.intentId) // kept for server-side reconciliation
        assertEquals("unknown", store.only?.state)
    }

    @Test fun collectSucceedingTooLateDoesNotConfirm() {
        reader.autoCollect = null
        reader.connectDelayMs = 5_000
        run()
        sched.advance(5_000)
        assertEquals(1, reader.collectIds.size)
        sched.advance(16_000) // t = 21s: 3s left of the 24s hard deadline, below the 4s confirm budget
        // (18s collect window ran out at 23s? no: it started at 5s, so still open)
        reader.collectCb!!(CollectOutcome.Collected)
        assertNull(reader.confirmCb)
        assertEquals(TapStatus.CANCELLED, last.status); assertEquals("timeout", last.reason)
        assertEquals(listOf("pi_1"), reader.cancelledIntents)
    }

    @Test fun confirmNeverReturnsIsUnknownAt24sNeverDeclined() {
        reader.autoConfirm = null
        run()
        assertEquals(0, sink.results.size)
        assertEquals("confirming", store.only?.state)
        sched.advance(23_999)
        assertEquals(0, sink.results.size)
        sched.advance(2)
        assertEquals(TapStatus.UNKNOWN, last.status); assertEquals("timeout", last.reason)
        assertEquals(0, reader.cancelledIntents.size) // never cancel a possibly-charged intent
        assertEquals("pi_1", store.only?.intentId)
    }

    @Test fun lateSuccessAfterUnknownCorrectsIt() {
        reader.autoConfirm = null
        run()
        sched.advance(24_001)
        assertEquals(TapStatus.UNKNOWN, last.status)
        reader.confirmCb!!(ConfirmOutcome.Succeeded("ch_1", null, null))
        assertEquals(2, sink.results.size)
        assertEquals(TapStatus.SUCCEEDED, last.status); assertTrue(last.late)
        assertNull(store.only)
    }

    @Test fun confirmNetworkErrorIsUnknown() {
        reader.autoConfirm = ConfirmOutcome.Failed("network")
        run()
        assertEquals(TapStatus.UNKNOWN, last.status); assertEquals("network", last.reason)
        assertEquals("pi_1", store.only?.intentId)
        assertTrue(reader.cancelledIntents.isEmpty())
    }

    @Test fun offlinePinIsAReportedDecline() {
        reader.autoConfirm = ConfirmOutcome.Declined(null, "offline_pin_required")
        run()
        assertEquals(TapStatus.DECLINED, last.status); assertEquals("offline_pin_required", last.reason)
    }

    @Test fun connectFailureRefusesAndCreatesNothing() {
        reader.connectError = "offline"
        run()
        assertEquals("offline", accepted)
        assertTrue(reader.createCalls.isEmpty()); assertTrue(sink.results.isEmpty()); assertNull(store.only)
    }

    @Test fun intentFailureRefusesAndWritesNoRecord() {
        reader.intentError = "connect_incomplete"
        run()
        assertEquals("connect_incomplete", accepted)
        assertNull(store.only); assertTrue(sink.results.isEmpty())
    }

    @Test fun acceptedIsAnsweredExactlyOnce() {
        run()
        sched.advance(60_000)
        assertEquals(1, acceptedCalls)
    }

    @Test fun collectReaderFailureCancelsWithItsReason() {
        reader.autoCollect = CollectOutcome.Failed("insecure_environment")
        run()
        assertEquals(TapStatus.CANCELLED, last.status); assertEquals("insecure_environment", last.reason)
    }

    @Test fun destroyWhileCollectingIsCancelledAppClosed() {
        reader.autoCollect = null
        val f = run()
        f.destroy()
        assertEquals(TapStatus.CANCELLED, last.status); assertEquals("app_closed", last.reason)
    }

    @Test fun destroyWhileConfirmingIsUnknownAppClosed() {
        reader.autoConfirm = null
        val f = run()
        f.destroy()
        assertEquals(TapStatus.UNKNOWN, last.status); assertEquals("app_closed", last.reason)
        assertTrue(reader.cancelledIntents.isEmpty())
    }

    @Test fun cancelAfterConfirmBeganIsTooLate() {
        reader.autoConfirm = null
        val f = run()
        assertFalse(f.cancel())
    }

    @Test fun lateIntentAfterHardDeadlineIsCancelledNotLeaked() {
        reader.connectDelayMs = 30_000
        run()
        sched.advance(24_001)
        assertEquals("offline", accepted)
        sched.advance(10_000)
        assertTrue(reader.createCalls.isEmpty()) // connect finished after we gave up: nothing created
        assertTrue(sink.results.isEmpty())
    }

    @Test fun cancelIntentNeverCallingBackEndsUnknownAndFreesTheFlow() {
        reader.autoCollect = null
        reader.cancelIntentHang = true
        val f = run()
        sched.advance(18_001)
        assertEquals(0, sink.results.size)
        assertTrue(f.isLive)
        sched.advance(TapToPayFlow.CANCEL_DEADLINE_MS + 1)
        assertEquals(TapStatus.UNKNOWN, last.status); assertEquals("timeout", last.reason)
        assertFalse(f.isLive)
        assertEquals("unknown", store.only?.state)
    }

    @Test fun timeoutCancelsCollectFirstThenTheIntent() {
        reader.autoCollect = null
        run()
        sched.advance(18_001)
        assertEquals(listOf("collect-cancel", "intent-cancel"), reader.order)
    }

    @Test fun intentCancelWaitsForCollectCancelButNotForever() {
        reader.autoCollect = null
        reader.collectCancelNever = true
        run()
        sched.advance(18_001)
        assertEquals(listOf("collect-cancel"), reader.order)
        sched.advance(TapToPayFlow.CANCEL_STEP_WAIT_MS + 1)
        assertEquals(listOf("collect-cancel", "intent-cancel"), reader.order)
        assertEquals(TapStatus.CANCELLED, last.status)
    }

    @Test fun lateResultOfOneKeyNeverTouchesAnotherKeysRecord() {
        reader.autoConfirm = null
        val a = flow(r = StartRequest("rq-a", 500L, "gbp", "key-a"))
        a.start { }
        sched.advance(24_001)
        assertEquals("unknown", store.load("key-a")?.state)
        val confirmA = reader.confirmCb!!
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        flow(r = StartRequest("rq-b", 700L, "gbp", "key-b")).start { }
        assertNull(store.load("key-b"))
        assertEquals("unknown", store.load("key-a")?.state)
        confirmA(ConfirmOutcome.Succeeded("ch_a", null, null))
        assertNull(store.load("key-a"))
    }

    @Test fun recordCarriesAmountAndCurrency() {
        reader.autoCollect = null
        run()
        assertEquals(1250L, store.only?.amountMinor); assertEquals("gbp", store.only?.currency)
    }
}
