// Every branch of the Tap to Pay coordinator (ut-docs#3049, design §4.3):
// the collect state machine, cancel/deadline handling, the never-declined-
// when-unknown rule (#6439), setup/prewarm terms handling, support reasons.
import XCTest

final class TapToPayCoordinatorTests: XCTestCase {
    var reader: FakeReader!
    var cloud: FakeCloud!
    var clock: FakeClock!
    var location: FakeLocation!
    var education: FakeEducation!
    var store: FakeStore!
    var coordinator: TapToPayCoordinator!
    var results: [CollectResult] = []

    override func setUp() {
        super.setUp()
        makeFixtures()
    }

    /// Fresh fakes and coordinator. `configure` runs BEFORE the coordinator
    /// exists: its init schedules a capability refresh on its queue that reads
    /// the fake reader, so mutating the reader afterwards is a data race.
    func makeFixtures(configure: (FakeReader) -> Void = { _ in }) {
        reader = FakeReader()
        cloud = FakeCloud()
        clock = FakeClock()
        location = FakeLocation()
        education = FakeEducation()
        store = FakeStore()
        results = []
        configure(reader)
        coordinator = TapToPayCoordinator(reader: reader, cloud: cloud, clock: clock, location: location,
                                          education: education, store: store, runBlocking: { $0() })
        coordinator.flush()
    }

    func request(amount: Int64 = 1000, currency: String = "gbp", key: String = "k-1",
                 deadlineIn ms: Int64 = 120_000) -> CollectRequest {
        CollectRequest(provider: "stripe", amountMinor: amount, currency: currency, idempotencyKey: key,
                       description: "Sale 42", deadlineUnixMs: clock.now + ms)
    }

    func collect(_ req: CollectRequest? = nil) {
        coordinator.collect(req ?? request()) { [unowned self] r in self.results.append(r) }
        coordinator.flush()
    }

    var only: CollectResult? {
        XCTAssertLessThanOrEqual(results.count, 1, "a collect must complete exactly once")
        return results.first
    }

    // MARK: approved / declined

    func testApproved() {
        collect()
        XCTAssertEqual(only, CollectResult(status: .approved, providerTxnId: "pi_1", chargeId: "ch_1",
                                           cardBrand: "visa", last4: "4242"))
        XCTAssertEqual(reader.collectCalls, 1)
        XCTAssertEqual(reader.confirmCalls, 1)
        XCTAssertEqual(reader.retrieveCalls, ["pi_1_secret_x"])
        let create = cloud.calls.first { $0.op == "session_create" }?.body
        XCTAssertEqual(create?["amount_minor"] as? Int64, 1000)
        XCTAssertEqual(create?["currency"] as? String, "gbp")
        XCTAssertEqual(create?["idempotency_key"] as? String, "k-1")
        XCTAssertEqual(create?["description"] as? String, "Sale 42")
    }

    func testDeclinedPassesTheDeclineCodeThrough() {
        reader.confirmOutcomes = [.failed(intent: FakeReader.intent(.requiresPaymentMethod), declineCode: "offline_pin_required")]
        collect()
        XCTAssertEqual(only?.status, .declined)
        XCTAssertEqual(only?.declineCode, "offline_pin_required")
        reader.confirmOutcomes = [.failed(intent: FakeReader.intent(.requiresPaymentMethod), declineCode: "generic_decline")]
        results = []
        collect(request(key: "k-2"))
        XCTAssertEqual(only?.status, .declined)
        XCTAssertEqual(only?.declineCode, "generic_decline")
    }

    // #6439: an unknown outcome is pending, never declined.
    func testConfirmWithUnknownIntentIsPendingNeverDeclined() {
        reader.confirmOutcomes = [.failed(intent: nil, declineCode: nil)]
        collect()
        XCTAssertEqual(only, .pending(TapToPayReason.outcomeUnknown))
    }

    func testConfirmSucceededButNotSucceededStatusIsPending() {
        reader.confirmOutcomes = [.succeeded(FakeReader.intent(.requiresCapture))]
        collect()
        XCTAssertEqual(only?.status, .pending)
    }

    func testRequiresConfirmationIsRetriedOnce() {
        reader.confirmOutcomes = [.failed(intent: FakeReader.intent(.requiresConfirmation), declineCode: nil),
                                  .succeeded(FakeReader.intent(.succeeded, charge: true))]
        collect()
        XCTAssertEqual(only?.status, .approved)
        XCTAssertEqual(reader.confirmCalls, 2)
    }

    func testRequiresConfirmationTwiceIsPending() {
        reader.confirmOutcomes = [.failed(intent: FakeReader.intent(.requiresConfirmation), declineCode: nil),
                                  .failed(intent: FakeReader.intent(.requiresConfirmation), declineCode: nil),
                                  .succeeded(FakeReader.intent(.succeeded, charge: true))]
        collect()
        XCTAssertEqual(only?.status, .pending)
        XCTAssertEqual(reader.confirmCalls, 2, "confirm is retried once only")
    }

    func testRequiresConfirmationNotRetriedWithLittleTimeLeft() {
        reader.confirmOutcomes = [.failed(intent: FakeReader.intent(.requiresConfirmation), declineCode: nil),
                                  .succeeded(FakeReader.intent(.succeeded, charge: true))]
        // 7.9 s to the hard deadline: 4.9 s to the soft one, under the 5 s floor.
        collect(request(deadlineIn: 7_900))
        XCTAssertEqual(only?.status, .pending)
        XCTAssertEqual(reader.confirmCalls, 1)
    }

    // ADR §3b: a PaymentIntent that already succeeded is approved with no tap.
    func testAlreadySucceededIntentIsApprovedWithZeroCollects() {
        reader.retrieveResult = .success(FakeReader.intent(.succeeded, charge: true))
        collect()
        XCTAssertEqual(only, CollectResult(status: .approved, providerTxnId: "pi_1", chargeId: "ch_1",
                                           cardBrand: "visa", last4: "4242"))
        XCTAssertEqual(reader.collectCalls, 0)
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    // Review: the provider is already processing this PaymentIntent, so it
    // is neither tapped nor confirmed again; the plugin settles it through
    // session_status (#6439: unknown is pending).
    func testProcessingIntentIsPendingWithNoTapAndNoConfirm() {
        reader.retrieveResult = .success(FakeReader.intent(.processing))
        collect()
        XCTAssertEqual(only, .pending(TapToPayReason.outcomeUnknown))
        XCTAssertEqual(reader.collectCalls, 0)
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    // Review: money has (or may have) moved on this key for a different
    // amount. "Declined" would invite a second charge by another method, so
    // it is pending, and nothing is tapped.
    func testAmountMismatchOnAnIntentThatMovedMoneyIsPendingNeverDeclined() {
        for status in [IntentStatus.succeeded, .processing, .requiresCapture, .unknown] {
            makeFixtures()
            reader.retrieveResult = .success(FakeReader.intent(status, amount: 999, charge: true))
            collect()
            XCTAssertEqual(only, .pending(TapToPayReason.amountMismatch), "\(status)")
            XCTAssertEqual(reader.collectCalls, 0)
            XCTAssertEqual(reader.confirmCalls, 0)
        }
    }

    func testCloudAmountMismatchOnASucceededIntentIsPending() {
        cloud.responses["session_create"] = .success(["id": "pi_1", "client_secret": "s", "status": "succeeded",
                                                      "amount_minor": 5, "currency": "gbp"])
        reader.retrieveResult = .success(FakeReader.intent(.succeeded, charge: true))
        collect()
        XCTAssertEqual(only, .pending(TapToPayReason.amountMismatch))
        XCTAssertEqual(reader.collectCalls, 0)
    }

    func testStatusObserverIsToldTheCurrentCapabilityAtOnce() {
        var seen: [Capability] = []
        coordinator.observeStatus { cap, _ in seen.append(cap) }
        coordinator.flush()
        XCTAssertEqual(seen, [.of(.needsSetup)])
    }

    func testCanceledIntentIsDeclined() {
        reader.retrieveResult = .success(FakeReader.intent(.canceled))
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.intentCanceled))
        XCTAssertEqual(reader.collectCalls, 0)
    }

    func testAmountMismatchCollectsNothing() {
        reader.retrieveResult = .success(FakeReader.intent(.requiresPaymentMethod, amount: 999))
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.amountMismatch))
        XCTAssertEqual(reader.collectCalls, 0)
    }

    func testCurrencyMismatchCollectsNothing() {
        reader.retrieveResult = .success(FakeReader.intent(.requiresPaymentMethod, currency: "eur"))
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.amountMismatch))
        XCTAssertEqual(reader.collectCalls, 0)
    }

    func testCloudAmountMismatchCollectsNothing() {
        cloud.responses["session_create"] = .success(["id": "pi_1", "client_secret": "s", "status": "requires_payment_method",
                                                      "amount_minor": 5, "currency": "gbp"])
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.amountMismatch))
        XCTAssertEqual(reader.collectCalls, 0)
    }

    func testCurrencyCaseIsIgnored() {
        reader.retrieveResult = .success(FakeReader.intent(.requiresPaymentMethod, currency: "GBP"))
        collect(request(currency: "gbp"))
        XCTAssertEqual(only?.status, .approved)
    }

    func testCloudFailureIsDeclinedWithItsReason() {
        cloud.responses["session_create"] = .failure(CloudError(code: "not_onboarded"))
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.notOnboarded))
        XCTAssertEqual(reader.collectCalls, 0)
    }

    func testRetrieveFailureIsPending() {
        reader.retrieveResult = .failure(.offline)
        collect()
        XCTAssertEqual(only?.status, .pending)
        XCTAssertEqual(reader.collectCalls, 0)
    }

    // MARK: cancel

    func testCustomerCancel() {
        reader.collectResult = .failure(.canceledByReader)
        collect()
        XCTAssertEqual(only, .cancelled(TapToPayReason.customer))
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    func testOtherCollectErrorIsReadFailed() {
        reader.collectResult = .failure(.other("card read"))
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.readFailed))
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    func testOperatorCancelWaitsForTheCancelCompletion() {
        reader.collectResult = nil // hold the collect open
        reader.cancelable.holdDone = true
        collect()
        XCTAssertEqual(reader.collectCalls, 1)
        XCTAssertNil(only)

        coordinator.cancel(idempotencyKey: "other-key")
        coordinator.flush()
        XCTAssertEqual(reader.cancelable.cancelCalls, 0, "a cancel for another key is a no-op")

        coordinator.cancel(idempotencyKey: "k-1")
        coordinator.flush()
        XCTAssertEqual(reader.cancelable.cancelCalls, 1)
        reader.finishCollect(.failure(.canceledByUs))
        coordinator.flush()
        XCTAssertNil(only, "no result until the cancel itself has completed")
        reader.cancelable.releaseCancel()
        coordinator.flush()
        XCTAssertEqual(only, .cancelled(TapToPayReason.operator))
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    func testCollectThatSucceedsAfterACancelIsNeverConfirmed() {
        reader.collectResult = nil
        collect()
        coordinator.cancel(idempotencyKey: "k-1")
        coordinator.flush()
        reader.finishCollect(.success(FakeReader.intent(.requiresConfirmation)))
        coordinator.flush()
        XCTAssertEqual(only, .cancelled(TapToPayReason.operator))
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    func testCancelBeforeAnyCollectIsANoOp() {
        coordinator.cancel(idempotencyKey: "k-1")
        coordinator.flush()
        XCTAssertEqual(reader.cancelable.cancelCalls, 0)
    }

    func testOperatorCancelWhileConnectingEndsTheAttempt() {
        reader.holdConnect = true
        collect()
        coordinator.cancel(idempotencyKey: "k-1")
        coordinator.flush()
        XCTAssertEqual(only, .cancelled(TapToPayReason.operator))
        reader.releaseConnect()
        coordinator.flush()
        XCTAssertEqual(results.count, 1)
        XCTAssertEqual(cloud.count("session_create"), 0)
    }

    // MARK: deadline

    func testDeadlineDuringCollectCancelsAndIsCancelledTimeout() {
        reader.collectResult = nil
        reader.cancelable.onCancel = { [unowned self] in self.reader.finishCollect(.failure(.canceledByUs)) }
        let req = request(deadlineIn: 60_000)
        collect(req)
        clock.advance(to: req.deadlineUnixMs - 3_001)
        coordinator.flush()
        XCTAssertEqual(reader.cancelable.cancelCalls, 0, "not before deadline − 3 s")
        clock.advance(to: req.deadlineUnixMs - 3_000)
        coordinator.flush()
        XCTAssertEqual(reader.cancelable.cancelCalls, 1)
        XCTAssertEqual(only, .cancelled(TapToPayReason.timeout))
    }

    func testDeadlineDuringConfirmIsPendingAndConfirmFinishesInBackground() {
        reader.holdConfirm = true
        let req = request(deadlineIn: 60_000)
        collect(req)
        XCTAssertEqual(reader.confirmCalls, 1)
        clock.advance(to: req.deadlineUnixMs - 3_000)
        coordinator.flush()
        XCTAssertEqual(only, .pending(TapToPayReason.timeout))
        reader.finishConfirm(.succeeded(FakeReader.intent(.succeeded, charge: true)))
        coordinator.flush()
        XCTAssertEqual(results.count, 1, "the late confirm outcome is logged only")
        // The slot is free again.
        reader.holdConfirm = false
        results = []
        collect(request(key: "k-2"))
        XCTAssertEqual(only?.status, .approved)
    }

    func testDeadlineWhileConnectingCreatesNothing() {
        reader.holdConnect = true
        let req = request(deadlineIn: 60_000)
        collect(req)
        clock.advance(to: req.deadlineUnixMs - 3_000)
        coordinator.flush()
        XCTAssertEqual(only, .cancelled(TapToPayReason.timeout))
        XCTAssertEqual(cloud.count("session_create"), 0)
    }

    // MARK: busy

    func testSecondCollectWhileOneIsInFlightIsBusy() {
        reader.collectResult = nil
        collect()
        collect(request(key: "k-2"))
        XCTAssertEqual(only, .declined(TapToPayReason.busy))
        XCTAssertEqual(reader.collectCalls, 1)
    }

    // MARK: terms of service

    func testCollectNeverAllowsTermsAcceptance() {
        collect()
        XCTAssertEqual(reader.connectCalls.count, 1)
        XCTAssertEqual(reader.connectCalls.first?.allowTerms, false)
        XCTAssertEqual(reader.connectCalls.first?.locationId, "tml_1")
    }

    func testCollectWhenTermsNotAcceptedIsDeclinedAndNeedsSetup() {
        reader.connectError = .termsNotAccepted
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.termsNotAccepted))
        XCTAssertEqual(coordinator.capability().state, .needsSetup)
        XCTAssertEqual(coordinator.capability().reason, TapToPayReason.termsNotAccepted)
    }

    func testCollectNotOnboarded() {
        cloud.responses["onboarding_status"] = .success(["onboarded": true, "charges_enabled": false, "location_id": "tml_1"])
        collect()
        XCTAssertEqual(only, .declined(TapToPayReason.notOnboarded))
        XCTAssertTrue(reader.connectCalls.isEmpty)
    }

    func testCollectSkipsConnectWhenConnected() {
        reader.connected = true
        collect()
        XCTAssertTrue(reader.connectCalls.isEmpty)
        XCTAssertEqual(cloud.count("onboarding_status"), 0)
        XCTAssertEqual(only?.status, .approved)
    }

    func testSetupAllowsTermsAndPresentsEducationOnFirstSuccess() {
        var outcomes: [SetupOutcome] = []
        coordinator.setup { outcomes.append($0) }
        coordinator.flush()
        XCTAssertEqual(outcomes, [SetupOutcome(ok: true, reason: nil)])
        XCTAssertEqual(location.calls, 1)
        XCTAssertEqual(reader.connectCalls.map(\.allowTerms), [true])
        XCTAssertTrue(store.isSetupDone("stripe"))
        XCTAssertEqual(education.presented, 1)
        XCTAssertEqual(coordinator.capability(), .of(.ready))

        coordinator.setup { outcomes.append($0) }
        coordinator.flush()
        XCTAssertEqual(outcomes.last, SetupOutcome(ok: true, reason: nil))
        XCTAssertEqual(education.presented, 1, "How to Tap opens automatically on the first success only")
    }

    func testSetupLocationDenied() {
        location.granted = false
        var outcome: SetupOutcome?
        coordinator.setup { outcome = $0 }
        coordinator.flush()
        XCTAssertEqual(outcome, SetupOutcome(ok: false, reason: TapToPayReason.locationDenied))
        XCTAssertTrue(reader.connectCalls.isEmpty)
        XCTAssertFalse(store.isSetupDone("stripe"))
    }

    func testSetupTermsDeclined() {
        reader.connectError = .termsNotAccepted
        var outcome: SetupOutcome?
        coordinator.setup { outcome = $0 }
        coordinator.flush()
        XCTAssertEqual(outcome, SetupOutcome(ok: false, reason: TapToPayReason.termsNotAccepted))
        XCTAssertFalse(store.isSetupDone("stripe"))
        XCTAssertEqual(education.presented, 0)
    }

    // ut-docs#6981: Setup keeps the native / cloud error code for support.
    private func runSetup() -> SetupOutcome? {
        var outcome: SetupOutcome?
        coordinator.setup { outcome = $0 }
        coordinator.flush()
        return outcome
    }

    func testSetupKeepsReaderErrorCode() {
        reader.connectError = .other("stripe 2900")
        XCTAssertEqual(runSetup(), SetupOutcome(ok: false, reason: TapToPayReason.other, detail: "stripe 2900"))
    }

    func testSetupCancelledByReader() {
        reader.connectError = .canceledByReader
        XCTAssertEqual(runSetup(), SetupOutcome(ok: false, reason: TapToPayReason.canceledByUser))
    }

    func testSetupPassesCloudCodes() {
        for code in ["insecure_endpoint", "device_role_forbidden", "unauthorized"] {
            cloud.responses["onboarding_status"] = .failure(CloudError(code: code))
            XCTAssertEqual(runSetup(), SetupOutcome(ok: false, reason: code, detail: code), code)
        }
        cloud.responses["onboarding_status"] = .failure(CloudError(code: "http_418"))
        XCTAssertEqual(runSetup(), SetupOutcome(ok: false, reason: TapToPayReason.other, detail: "http_418"))
    }

    func testPrewarmConnectsOnlyAfterSetupAndNeverAllowsTerms() {
        coordinator.prewarm()
        coordinator.flush()
        XCTAssertTrue(reader.connectCalls.isEmpty, "no prewarm before the manager's setup")
        store.markSetupDone("stripe")
        coordinator.prewarm()
        coordinator.flush()
        XCTAssertEqual(reader.connectCalls.map(\.allowTerms), [false])
        XCTAssertEqual(coordinator.capability(), .of(.ready))
        coordinator.prewarm()
        coordinator.flush()
        XCTAssertEqual(reader.connectCalls.count, 1, "no prewarm when already connected")
    }

    func testCollectJoinsAPrewarmConnectInFlight() {
        store.markSetupDone("stripe")
        reader.holdConnect = true
        coordinator.prewarm()
        coordinator.flush()
        XCTAssertEqual(coordinator.capability().state, .preparing)
        XCTAssertEqual(coordinator.capability().progress, 40)
        collect()
        XCTAssertEqual(reader.connectCalls.count, 1, "one connect at a time")
        reader.releaseConnect()
        coordinator.flush()
        XCTAssertEqual(only?.status, .approved)
    }

    // MARK: support reasons

    func testEverySupportReasonDeclinesWithoutTouchingAnything() {
        for reason in [TapToPayReason.ipad, TapToPayReason.unsupportedDevice, TapToPayReason.iosTooOld,
                       TapToPayReason.entitlementMissing] {
            makeFixtures { $0.supportResult = .unsupported(reason) }
            collect()
            XCTAssertEqual(only, .declined(reason), reason)
            var outcome: SetupOutcome?
            coordinator.setup { outcome = $0 }
            coordinator.flush()
            XCTAssertEqual(outcome, SetupOutcome(ok: false, reason: reason), reason)
            var edu: SetupOutcome?
            coordinator.showEducation { edu = $0 }
            coordinator.flush()
            XCTAssertEqual(edu, SetupOutcome(ok: false, reason: reason), reason)
            XCTAssertEqual(coordinator.capability(), .of(.unavailable, reason: reason), reason)
            XCTAssertTrue(cloud.calls.isEmpty, reason)
            XCTAssertTrue(reader.connectCalls.isEmpty, reason)
            XCTAssertEqual(location.calls, 0, reason)
        }
    }

    func testCapabilitiesJSON() throws {
        let json = coordinator.capabilitiesJSON()
        let obj = try JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any]
        let stripe = (obj?["tap_to_pay"] as? [String: Any])?["stripe"] as? [String: Any]
        XCTAssertEqual(stripe?["state"] as? String, "needs_setup")
        XCTAssertEqual(stripe?["ready"] as? Bool, false)
    }

    func testShowEducation() {
        var outcome: SetupOutcome?
        coordinator.showEducation { outcome = $0 }
        coordinator.flush()
        XCTAssertEqual(outcome, SetupOutcome(ok: true, reason: nil))
        XCTAssertEqual(education.presented, 1)
    }

    func testUnknownProviderOrBadRequestIsDeclined() {
        var req = request()
        req.provider = "sumup"
        collect(req)
        XCTAssertEqual(only, .declined(TapToPayReason.unsupportedProvider))
        results = []
        collect(request(amount: 0, key: "k-3"))
        XCTAssertEqual(only, .declined(TapToPayReason.badRequest))
        XCTAssertTrue(cloud.calls.isEmpty)
    }
}
