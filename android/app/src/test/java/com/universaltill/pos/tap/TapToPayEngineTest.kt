package com.universaltill.pos.tap

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
    private lateinit var done: MemoryDoneStore
    private lateinit var probe: FakeProbe
    private lateinit var ui: FakeUi
    private lateinit var engine: TapToPayEngine

    /** Every [CollectEnd] by request id: each collect must end exactly once. */
    private val ends = HashMap<String, MutableList<CollectEnd>>()

    @Before fun setUp() {
        sched = FakeScheduler(); reader = FakeReader(sched); store = MemoryPendingStore(); done = MemoryDoneStore()
        probe = FakeProbe(readyFacts()); ui = FakeUi()
        engine = TapToPayEngine(probe, reader, sched, store, done, ui)
        engine.onForeground()
    }

    private fun collect(id: String = "r1", key: String = "k1", amount: Long = 500, remainingMs: Long? = null, on: TapToPayEngine = engine) {
        on.collect(StartRequest(id, amount, "gbp", key), remainingMs) { ends.getOrPut(id) { mutableListOf() } += it }
    }

    private fun endOf(id: String): CollectEnd? {
        val l = ends[id].orEmpty()
        assertTrue("$id ended ${l.size} times", l.size <= 1)
        return l.singleOrNull()
    }

    private fun refusal(id: String) = (endOf(id) as CollectEnd.Refused).token

    private fun verdict(id: String) = (endOf(id) as CollectEnd.Verdict).result

    @Test fun collectSucceedsAndReportsTheVerdictOnce() {
        collect()
        assertEquals(TapStatus.SUCCEEDED, verdict("r1").status)
        assertEquals("r1", verdict("r1").requestId)
    }

    @Test fun singleFlightBusy() {
        reader.autoCollect = null
        collect("r1", "k1")
        assertNull(endOf("r1"))
        collect("r2", "k2")
        assertEquals("busy", refusal("r2"))
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun notForeground() {
        engine.onBackground()
        collect()
        assertEquals("not_foreground", refusal("r1"))
    }

    @Test fun backgroundDoesNotCancelAnInFlightPayment() {
        reader.autoCollect = null
        collect()
        engine.onBackground()
        engine.onForeground()
        assertEquals(0, reader.collectCancels)
        reader.collectCb!!(CollectOutcome.Collected)
        assertEquals(TapStatus.SUCCEEDED, verdict("r1").status)
    }

    @Test fun notReadyMapsFixableNeeds() {
        probe.facts = readyFacts().copy(educationAccepted = false)
        collect("a"); assertEquals("needs_education", refusal("a"))
        probe.facts = readyFacts().copy(online = false)
        collect("b"); assertEquals("offline", refusal("b"))
        probe.facts = readyFacts().copy(nfcEnabled = false)
        collect("c"); assertEquals("not_ready", refusal("c"))
        probe.facts = readyFacts().copy(hasNfc = false)
        collect("d"); assertEquals("not_ready", refusal("d"))
        assertTrue(reader.createCalls.isEmpty())
    }

    @Test fun connectProblemsSurfaceAsRefusals() {
        reader.connectError = "connect_incomplete"
        collect()
        assertEquals("connect_incomplete", refusal("r1"))
    }

    @Test fun cancelByKeyCancelsCollectOnly() {
        reader.autoCollect = null
        collect("r1", "k1")
        assertFalse("another key is not this payment", engine.cancel("other"))
        assertTrue(engine.cancel("k1"))
        assertEquals(TapStatus.CANCELLED, verdict("r1").status)
        assertFalse(engine.cancel("k1")) // nothing in flight any more
    }

    @Test fun cancelAfterConfirmIsTooLate() {
        reader.autoConfirm = null
        collect("r1", "k1")
        assertFalse(engine.cancel("k1"))
    }

    @Test fun theCallersDeadlineCancelsTheCollectBeforeGoGivesUp() {
        reader.autoCollect = null
        collect("r1", "k1", remainingMs = 10_000)
        sched.advance(10_000 - TapToPayEngine.DEADLINE_MARGIN_MS - 1)
        assertNull(endOf("r1"))
        sched.advance(2)
        assertEquals(TapStatus.CANCELLED, verdict("r1").status)
        assertEquals(1, reader.collectCancels)
    }

    @Test fun aDeadlineTimerOfAFinishedPaymentNeverTouchesTheNextOne() {
        collect("r1", "k1", remainingMs = 10_000) // finishes at once
        reader.autoCollect = null
        collect("r2", "k2")
        sched.advance(8_000) // past r1's old deadline (7 s), inside r2's own 18 s collect window
        assertNull(endOf("r2"))
        assertEquals(0, reader.collectCancels)
    }

    @Test fun unknownThenSameKeyReusesTheLiveIntentNeverCreatesASecond() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        assertEquals(TapStatus.UNKNOWN, verdict("r1").status)
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        collect("r2", "k1")
        assertEquals(1, reader.createCalls.size)
        assertEquals(listOf("pi_1", "pi_1"), reader.collectIds)
    }

    @Test fun retryOfAnUnknownKeyKeepsTheUnresolvedRecordWhileItCollects() {
        // Review #3048 R5: the retry used to overwrite the `unknown` record with `created`, which does not lock
        // anything, so a process death during the retry freed every other basket while the first tap may be charged.
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        assertEquals("unknown", store.load("k1")!!.state)
        reader.autoCollect = null
        collect("r2", "k1")
        assertNull(endOf("r2"))
        assertEquals("unknown", store.load("k1")!!.state)
        assertEquals("pi_1", store.load("k1")!!.intentId)
    }

    @Test fun unknownAfterProcessDeathIsPendingForTheSameKeyAndARefusalForOthers() {
        store.put(PendingRecord("old", "k1", "pi_77", "confirming", 500L, "gbp"))
        collect("r2", "k1")
        // this very payment may have been charged: the caller must settle it, not charge again
        assertEquals("outcome_unknown", refusal("r2"))
        assertTrue(reader.createCalls.isEmpty())
        // and, by design, every other basket waits until the operator resolves it
        collect("r3", "k2")
        assertEquals("unknown_pending", refusal("r3"))
    }

    @Test fun declinedKeepsTheIntentForTheSameKeyRetry() {
        reader.autoConfirm = ConfirmOutcome.Declined("card_declined")
        collect("r1", "k1")
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        collect("r2", "k1")
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun repeatingASucceededKeyAnswersWithTheStoredResultAndChargesNothing() {
        collect("r1", "k1")
        collect("r2", "k1")
        assertEquals(1, reader.createCalls.size)
        assertEquals(1, reader.collectIds.size)
        val v = verdict("r2")
        assertEquals(TapStatus.SUCCEEDED, v.status); assertEquals("r2", v.requestId); assertEquals("pi_1", v.paymentIntentId)
    }

    @Test fun succeededKeyWithADifferentAmountIsRefused() {
        collect("r1", "k1", amount = 500)
        collect("r2", "k1", amount = 900)
        assertEquals("key_mismatch", refusal("r2"))
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun aSucceededPaymentSurvivesTheEngineBeingRebuilt() {
        collect("r1", "k1")
        // a new Activity / a new engine over the same persisted stores
        val again = TapToPayEngine(probe, reader, sched, store, done, ui)
        again.onForeground()
        collect("r9", "k1", on = again)
        assertEquals(TapStatus.SUCCEEDED, verdict("r9").status)
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun sameKeyRetryWithADifferentAmountNeverCollectsTheStaleIntent() {
        reader.autoConfirm = ConfirmOutcome.Declined("card_declined")
        collect("r1", "k1", amount = 500)
        collect("r2", "k1", amount = 900)
        assertEquals("key_mismatch", refusal("r2"))
        assertEquals(1, reader.collectIds.size)
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun aDifferentKeyAfterUnknownIsRefusedUntilResolved() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        collect("r2", "k2")
        assertEquals("unknown_pending", refusal("r2"))
        assertEquals(1, reader.createCalls.size)
        assertNull(engine.resolve("k1", Outcome.NOT_CHARGED))
        collect("r3", "k2")
        assertEquals(TapStatus.SUCCEEDED, verdict("r3").status)
        assertEquals(2, reader.createCalls.size)
    }

    @Test fun resolveUnknownNeedsAKnownUnresolvedPayment() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        assertEquals("no_such_payment", engine.resolve("nope", Outcome.NOT_CHARGED))
        assertEquals("unknown", store.load("k1")?.state)
    }

    @Test fun resolveUnknownRefusesWhileThatPaymentIsStillLive() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        reader.autoConfirm = null
        reader.autoCollect = null
        collect("r2", "k1") // the retry is collecting
        assertEquals("busy", engine.resolve("k1", Outcome.NOT_CHARGED))
        assertEquals("unknown", store.load("k1")?.state)
    }

    @Test fun resolveChargedKeepsTheKeyPaidSoItIsNeverChargedAgain() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        assertNull(engine.resolve("k1", Outcome.CHARGED))
        assertNull(store.load("k1"))
        assertEquals("pi_1", done.load("k1")?.intentId)
        assertEquals(500L, done.load("k1")?.amountMinor)
        // the till is free for another basket, and the paid key answers its stored approval without a second intent
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        collect("r2", "k1")
        assertEquals(TapStatus.SUCCEEDED, verdict("r2").status)
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun resolveWithAnythingButTheTwoOutcomesChangesNothing() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        for (bad in listOf("", "refunded", "NOT_CHARGED", "charged ")) assertEquals("bad_request", engine.resolve("k1", bad))
        assertEquals("unknown", store.load("k1")?.state)
        assertNull(done.load("k1"))
    }

    @Test fun resolveChargedRefusesWhileThatPaymentIsStillLive() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        reader.autoCollect = null
        collect("r2", "k1")
        assertEquals("busy", engine.resolve("k1", Outcome.CHARGED))
        assertNull(done.load("k1"))
    }

    @Test fun declinedIntentsAreBoundedAndALatePaidWriteSurvivesTheEviction() {
        // Recheck of #3048 R3: liveIntents holds declined and unknown intents; it must not grow with the sale count.
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        val lateConfirm = reader.confirmCb!!
        assertNull(engine.resolve("k1", Outcome.NOT_CHARGED))
        reader.autoConfirm = ConfirmOutcome.Declined("card_declined")
        for (i in 2..150) collect("r$i", "k$i")
        assertTrue("live intents ${engine.liveIntentCount()}", engine.liveIntentCount() <= TapToPayEngine.MAX_LIVE_INTENTS)
        lateConfirm(ConfirmOutcome.Succeeded("ch_late", null, null))
        assertEquals("ch_late", done.load("k1")?.chargeId)
    }

    @Test fun unknownRecordFromAnEarlierProcessBlocksOtherKeysToo() {
        store.put(PendingRecord("old", "k1", "pi_77", "unknown", 500L, "gbp"))
        collect("r2", "k2")
        assertEquals("unknown_pending", refusal("r2"))
    }

    @Test fun processDeathBeforeConfirmDoesNotLockTheKey() {
        store.put(PendingRecord("old", "k1", "pi_77", "created", 500L, "gbp"))
        collect("r2", "k1")
        assertEquals(1, reader.createCalls.size)
        assertEquals(TapStatus.SUCCEEDED, verdict("r2").status)
    }

    @Test fun finishingActivityThenLateSuccessIsNotLost() {
        reader.autoConfirm = null
        collect("r1", "k1")
        engine.onDestroy(isFinishing = true)
        assertEquals(TapStatus.UNKNOWN, verdict("r1").status)
        reader.confirmCb!!(ConfirmOutcome.Succeeded("ch_1", null, null))
        assertEquals("ch_1", done.load("k1")?.chargeId)
        assertNull(store.load("k1"))
        assertEquals("the caller is told once; the late success is only persisted", 1, ends["r1"]!!.size)
    }

    @Test fun aLateSuccessIsStillPersistedAfterManyMorePaymentsRan() {
        // Follow-up of #3048: a shared per-request map was cleared at 64 entries and the paid write went with it.
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        val lateConfirm = reader.confirmCb!!
        assertEquals(TapStatus.UNKNOWN, verdict("r1").status)
        assertNull(engine.resolve("k1", Outcome.NOT_CHARGED)) // the operator frees the till; the first payment is still in flight at Stripe
        reader.autoConfirm = ConfirmOutcome.Succeeded(null, null, null)
        for (i in 2..70) collect("r$i", "k$i")
        lateConfirm(ConfirmOutcome.Succeeded("ch_late", null, null))
        assertEquals("ch_late", done.load("k1")?.chargeId)
        assertEquals(500L, done.load("k1")?.amountMinor)
    }

    @Test fun startErrorsUseOnlyDocumentedTokens() {
        reader.connectError = "network"
        collect("a"); assertEquals("offline", refusal("a"))
        reader.connectError = "SOMETHING_ELSE"
        collect("b"); assertEquals("not_ready", refusal("b"))
        reader.connectError = "insecure_environment"
        collect("c"); assertEquals("insecure_environment", refusal("c"))
        reader.connectError = "connect_incomplete"
        collect("d"); assertEquals("connect_incomplete", refusal("d"))
        reader.connectError = "intent_canceled"
        collect("e"); assertEquals("intent_canceled", refusal("e"))
    }

    @Test fun destroyWhileFinishingCancelsCollect() {
        reader.autoCollect = null
        collect()
        engine.onDestroy(isFinishing = true)
        assertEquals("app_closed", verdict("r1").reason)
    }

    @Test fun destroyForRotationDoesNotCancel() {
        reader.autoCollect = null
        collect()
        engine.onDestroy(isFinishing = false)
        assertEquals(0, reader.collectCancels)
    }

    @Test fun educationReportsWhatTheOperatorAnswered() {
        var got: Boolean? = null
        engine.showEducation { got = it }
        assertEquals(true, got)
        ui.educationAnswer = false
        engine.showEducation { got = it }
        assertEquals(false, got)
    }

    private fun setup(): List<String?> {
        val r = mutableListOf<String?>()
        var called = 0
        engine.setup { called++; r += it }
        assertEquals("setup answers exactly once", 1, called)
        return r
    }

    @Test fun setupWarmsTheReaderAndReportsReady() {
        assertEquals(listOf<String?>(null), setup())
        assertFalse(engine.isPreparing)
        assertEquals(0, ui.permissionAsked)
    }

    @Test fun setupAsksLocationPermissionAfterRationaleAndStopsWhenDenied() {
        probe.facts = readyFacts().copy(locationPermission = false)
        ui.permissionAnswer = false
        assertEquals(listOf<String?>("location_denied"), setup())
        assertEquals(1, ui.permissionAsked)
        assertTrue(reader.createCalls.isEmpty())
        assertFalse(engine.isPreparing)
    }

    @Test fun setupTrustsAGrantedPermissionAndShowsTheEducationOnce() {
        probe.facts = readyFacts().copy(locationPermission = false, educationAccepted = false)
        var edu = 0
        val ui2 = object : UiActions by ui {
            override fun showEducation(cb: (Boolean) -> Unit) { edu++; cb(true) }
        }
        val e = TapToPayEngine(probe, reader, sched, store, done, ui2)
        var out: String? = "unset"
        e.setup { out = it }
        assertNull(out)
        assertEquals(1, edu)
        assertEquals(1, ui.permissionAsked)
    }

    @Test fun setupStopsWhenTheEducationIsDeclined() {
        probe.facts = readyFacts().copy(educationAccepted = false)
        ui.educationAnswer = false
        assertEquals(listOf<String?>("education_declined"), setup())
    }

    @Test fun setupOpensTheRightSettingsScreenWhenNfcOrLocationIsOff() {
        probe.facts = readyFacts().copy(nfcEnabled = false)
        assertEquals(listOf<String?>("nfc_off"), setup())
        probe.facts = readyFacts().copy(locationServicesOn = false)
        assertEquals(listOf<String?>("location_off"), setup())
        assertEquals(listOf("nfc", "location"), ui.opened)
    }

    @Test fun setupOnAnUnsupportedDeviceNamesWhy() {
        probe.facts = readyFacts().copy(hasNfc = false)
        assertEquals(listOf<String?>("no_nfc"), setup())
        probe.facts = readyFacts().copy(flagOn = false)
        assertEquals(listOf<String?>("flag_off"), setup())
        assertEquals(0, ui.permissionAsked)
    }

    @Test fun setupReportsConnectFailuresWithDocumentedTokens() {
        reader.connectError = "connect_incomplete"
        assertEquals(listOf<String?>("connect_incomplete"), setup())
        reader.connectError = "weird"
        assertEquals(listOf<String?>("not_ready"), setup())
    }

    @Test fun setupIsBusyWhileItRunsOrAPaymentIsLive() {
        reader.connectDelayMs = 1_000
        engine.setup {}
        assertTrue(engine.isPreparing)
        assertEquals(listOf<String?>("busy"), setup())
        sched.advance(1_000)
        assertFalse(engine.isPreparing)
        reader.autoCollect = null
        collect()
        assertEquals(listOf<String?>("busy"), setup())
    }

    @Test fun capabilityReflectsTheFlagOff() {
        probe.facts = readyFacts().copy(flagOn = false)
        val c = engine.capability()
        assertFalse(c.supported); assertEquals("flag_off", c.reason)
    }

    // ---- review A1: a repeated key that may have been charged is never answered "declined" ----

    @Test fun sameKeyWhileItsFirstPaymentIsStillLiveIsPendingNotBusy() {
        reader.autoConfirm = null
        collect("r1", "k1")
        collect("r2", "k1")
        assertEquals("outcome_unknown", refusal("r2"))
        assertEquals(1, reader.createCalls.size)
        collect("r3", "k2")
        assertEquals("busy", refusal("r3")) // a different key is still plainly busy
    }

    @Test fun sameUnresolvedKeyWhileOfflineIsPendingNotOffline() {
        reader.autoConfirm = null
        collect("r1", "k1")
        sched.advance(24_001)
        assertEquals(TapStatus.UNKNOWN, verdict("r1").status)
        probe.facts = readyFacts().copy(online = false)
        collect("r2", "k1")
        assertEquals("outcome_unknown", refusal("r2"))
    }

    @Test fun sameUnresolvedKeyWhileBackgroundedIsPendingNotNotForeground() {
        store.put(PendingRecord("old", "k1", "pi_77", "unknown", 500L, "gbp"))
        engine.onBackground()
        collect("r2", "k1")
        assertEquals("outcome_unknown", refusal("r2"))
        collect("r3", "k2")
        assertEquals("not_foreground", refusal("r3"))
    }

    @Test fun aPaidKeyAnswersTheStoredApprovalEvenOffline() {
        collect("r1", "k1")
        assertEquals(TapStatus.SUCCEEDED, verdict("r1").status)
        probe.facts = readyFacts().copy(online = false)
        engine.onBackground()
        collect("r2", "k1")
        assertEquals(TapStatus.SUCCEEDED, verdict("r2").status)
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun aPaidKeyAnswersTheStoredApprovalWhileAnotherPaymentIsBusy() {
        collect("r1", "k1")
        reader.autoCollect = null
        collect("r2", "k2")
        collect("r3", "k1")
        assertEquals(TapStatus.SUCCEEDED, verdict("r3").status)
    }

    // ---- review A2: Go's deadline and cancel also bind a flow that is still starting ----

    @Test fun theCallersDeadlineAlsoCoversASlowStartSoNoTapSheetOpensForAGivenUpPayment() {
        reader.connectDelayMs = 20_000
        collect("r1", "k1", remainingMs = 10_000)
        sched.advance(10_000 - TapToPayEngine.DEADLINE_MARGIN_MS + 1)
        assertEquals("intent_canceled", refusal("r1"))
        sched.advance(15_000) // the connect finally completes: nothing may start
        assertTrue(reader.createCalls.isEmpty())
        assertTrue(reader.collectIds.isEmpty())
        assertTrue(store.map.isEmpty())
    }

    @Test fun goCancelAbortsAStartingFlow() {
        reader.connectDelayMs = 5_000
        collect("r1", "k1")
        assertTrue(engine.cancel("k1"))
        assertEquals("intent_canceled", refusal("r1"))
        sched.advance(6_000)
        assertTrue(reader.collectIds.isEmpty())
        // the engine is free again
        reader.connectDelayMs = 0
        collect("r2", "k2")
        assertEquals(TapStatus.SUCCEEDED, verdict("r2").status)
    }
}
