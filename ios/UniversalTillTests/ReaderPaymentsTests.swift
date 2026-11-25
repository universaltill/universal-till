// The outcome mapper table (design 2.2), the local attempt record, the token
// routing rule and the build gate.
import XCTest

final class ReaderOutcomeMapperTests: XCTestCase {
    func testApproved() {
        XCTAssertEqual(ReaderOutcomeMapper.map(CollectResult(status: .approved, providerTxnId: "pi_1")), .result(.approved))
    }

    func testCardDeclineKeepsItsCode() {
        XCTAssertEqual(ReaderOutcomeMapper.map(CollectResult(status: .declined, declineCode: "insufficient_funds")),
                       .result(.declined(code: "insufficient_funds")))
    }

    func testAnUnshapedDeclineCodeIsDropped() {
        XCTAssertEqual(ReaderOutcomeMapper.map(CollectResult(status: .declined, declineCode: "Not A Slug")), .result(.declined(code: nil)))
    }

    func testAmountMismatchIsPendingNeverDeclined() {
        XCTAssertEqual(ReaderOutcomeMapper.map(.declined(TapToPayReason.amountMismatch)), .result(.pending(reason: "amount_mismatch")))
    }

    func testCancelled() {
        XCTAssertEqual(ReaderOutcomeMapper.map(.cancelled(TapToPayReason.operator)), .result(.cancelled))
    }

    func testPendingKeepsAKnownReasonElsePhonePending() {
        XCTAssertEqual(ReaderOutcomeMapper.map(.pending(TapToPayReason.timeout)), .result(.pending(reason: "timeout")))
        XCTAssertEqual(ReaderOutcomeMapper.map(.pending("something_new")), .result(.pending(reason: "phone_pending")))
    }

    func testPreCollectReasonsAreNotStarted() {
        for r in [TapToPayReason.locationDenied, TapToPayReason.termsNotAccepted, TapToPayReason.entitlementMissing,
                  TapToPayReason.unsupportedDevice, TapToPayReason.notOnboarded, TapToPayReason.readFailed, TapToPayReason.busy] {
            XCTAssertEqual(ReaderOutcomeMapper.map(.declined(r)), .notStarted, r)
        }
    }

    func testAnUnknownDeclineReasonIsPending() {
        XCTAssertEqual(ReaderOutcomeMapper.map(.declined("brand_new_reason")), .result(.pending(reason: "phone_pending")))
    }
}

final class ReaderTokenRoutingTests: XCTestCase {
    func testRuleTable() {
        typealias R = ReaderTokenRouting
        XCTAssertEqual(R.select(owner: .till, readerEnrolled: true, linked: true), .cloud, "the till's own tap uses the till's credential")
        XCTAssertEqual(R.select(owner: .reader, readerEnrolled: false, linked: false), .link)
        XCTAssertEqual(R.select(owner: .none, readerEnrolled: true, linked: true), .link)
        XCTAssertEqual(R.select(owner: .none, readerEnrolled: true, linked: false), .cloud)
        XCTAssertEqual(R.select(owner: .none, readerEnrolled: false, linked: true), .cloud)
        XCTAssertEqual(R.select(owner: .none, readerEnrolled: false, linked: false), .cloud)
    }
}

final class ReaderFeatureTests: XCTestCase {
    func testOffByDefault() {
        XCTAssertFalse(ReaderFeature.isEnabled(info: nil))
        XCTAssertFalse(ReaderFeature.isEnabled(info: [:]))
        XCTAssertFalse(ReaderFeature.isEnabled(info: ["UTReaderModeEnabled": "NO", "UTTapToPayEnabled": "YES"]))
    }

    func testNeedsTapToPayToo() {
        XCTAssertFalse(ReaderFeature.isEnabled(info: ["UTReaderModeEnabled": "YES"]))
        XCTAssertFalse(ReaderFeature.isEnabled(info: ["UTReaderModeEnabled": "YES", "UTTapToPayEnabled": "NO"]))
        XCTAssertTrue(ReaderFeature.isEnabled(info: ["UTReaderModeEnabled": "YES", "UTTapToPayEnabled": "YES"]))
    }

    func testOnlyTheExactStringYesTurnsItOn() {
        for v in ["yes", "true", "1", "Y", ""] {
            XCTAssertFalse(ReaderFeature.isEnabled(info: ["UTReaderModeEnabled": v, "UTTapToPayEnabled": "YES"]), v)
        }
    }

    func testFlagOffNeverTouchesTheStore() {
        var built = 0
        let store = ReaderFeature.makeStore(info: ["UTReaderModeEnabled": "NO", "UTTapToPayEnabled": "YES"]) { built += 1; return MemoryReaderStore() }
        XCTAssertNil(store)
        XCTAssertEqual(built, 0, "no store, so no Keychain read or write")
    }

    func testFlagOnBuildsTheStore() {
        let store = ReaderFeature.makeStore(info: ["UTReaderModeEnabled": "YES", "UTTapToPayEnabled": "YES"]) { MemoryReaderStore() }
        XCTAssertNotNil(store)
    }

    func testCredentialsNeverPrintTheirSecrets() {
        let c = ReaderCredentials(address: "h:1", readerId: "rd_1", bearer: "BEARER-XYZ", pin: "PIN-XYZ")
        XCTAssertFalse("\(c)".contains("BEARER-XYZ"))
        XCTAssertFalse("\(c)".contains("PIN-XYZ"))
    }
}

/// CoordinatorReaderPayments + the REAL coordinator on the fake Stripe reader.
final class CoordinatorReaderPaymentsTests: XCTestCase {
    var reader: FakeReader!
    var cloud: FakeCloud!
    var clock: FakeClock!
    var coordinator: TapToPayCoordinator!
    var payments: CoordinatorReaderPayments!
    let sq = DispatchQueue(label: "test.reader.payments")
    var starts: [Bool] = []
    var ends: [ReaderOutcome] = []

    override func setUp() {
        super.setUp()
        reader = FakeReader()
        cloud = FakeCloud()
        clock = FakeClock()
        reader.collectResult = nil   // the tap is held until the test finishes it
        coordinator = TapToPayCoordinator(reader: reader, cloud: cloud, clock: clock, location: FakeLocation(),
                                          education: FakeEducation(), store: FakeStore(), runBlocking: { $0() })
        coordinator.flush()
        payments = CoordinatorReaderPayments(coordinator: coordinator, clock: clock, sessionQueue: sq)
        coordinator.flush()
    }

    func collect(key: String = "k-1", amount: Int64 = 1000, currency: String = "gbp") {
        let c = ReaderCollect(amountMinor: amount, currency: currency, key: key, timeoutMs: 110_000,
                              clientSecret: "pi_1_secret_x", locationId: "tml_9")
        sq.sync {
            payments.collect(c, remainingMs: 110_000, started: { self.starts.append($0) }, end: { self.ends.append($0) })
        }
        coordinator.flush(); sq.sync {}; sq.sync {}
    }

    func testARefusalBeforeTheLockIsNotStartedAndNeverAResultTheMainWouldTimeOutOn() {
        collect(currency: "gb")   // bad request: refused before the coordinator takes its lock
        XCTAssertEqual(starts, [false])
        XCTAssertTrue(ends.isEmpty)
        XCTAssertTrue(reader.retrieveCalls.isEmpty)
    }

    func testReaderCollectSkipsCreateIntentAndRetrievesBySecret() {
        reader.confirmOutcomes = [.succeeded(FakeReader.intent(.succeeded, charge: true))]
        collect()
        reader.finishCollect(.success(FakeReader.intent(.requiresConfirmation)))
        coordinator.flush(); sq.sync {}; sq.sync {}
        XCTAssertEqual(cloud.count("session_create"), 0, "the main made the PaymentIntent")
        XCTAssertEqual(reader.retrieveCalls, ["pi_1_secret_x"])
        XCTAssertEqual(starts, [true])
        XCTAssertEqual(ends, [.result(.approved)])
    }

    func testReaderCollectConnectsToTheFramesLocationWithoutACloudLookup() {
        collect()
        XCTAssertEqual(reader.connectCalls.first?.locationId, "tml_9")
        XCTAssertEqual(cloud.count("onboarding_status"), 0)
        XCTAssertEqual(reader.connectCalls.first?.allowTerms, false)
    }

    func testOwnCollectWhileReaderCollectingIsBusy() {
        collect()
        var till: [CollectResult] = []
        coordinator.collect(CollectRequest(provider: "stripe", amountMinor: 500, currency: "gbp", idempotencyKey: "own-1",
                                           description: nil, deadlineUnixMs: clock.now + 100_000)) { till.append($0) }
        coordinator.flush()
        XCTAssertEqual(till, [.declined(TapToPayReason.busy)])
        XCTAssertEqual(coordinator.lockOwner(), .reader)
    }

    func testReaderCollectWhileOwnCollectingIsBusyAndStartsNothing() {
        reader.holdConnect = true
        coordinator.collect(CollectRequest(provider: "stripe", amountMinor: 500, currency: "gbp", idempotencyKey: "own-1",
                                           description: nil, deadlineUnixMs: clock.now + 100_000)) { _ in }
        coordinator.flush()
        collect()
        XCTAssertEqual(starts, [false])
        XCTAssertTrue(ends.isEmpty)
        XCTAssertTrue(reader.retrieveCalls.isEmpty)
        XCTAssertEqual(coordinator.lockOwner(), .till)
    }

    func testBusyCallbackFiresOnTakeAndReleaseForBothOrigins() {
        var seen: [TapToPayCoordinator.Origin?] = []
        let lock = NSLock()
        coordinator.observeBusy { o in lock.lock(); seen.append(o); lock.unlock() }
        coordinator.flush()
        collect()
        reader.finishCollect(.failure(.canceledByReader))
        coordinator.flush()
        coordinator.collect(CollectRequest(provider: "stripe", amountMinor: 500, currency: "gbp", idempotencyKey: "own-1",
                                           description: nil, deadlineUnixMs: clock.now + 100_000)) { _ in }
        coordinator.flush()
        coordinator.cancel(idempotencyKey: "own-1")
        coordinator.flush()
        lock.lock(); defer { lock.unlock() }
        XCTAssertEqual(seen, [nil, .reader, nil, .till, nil])
    }

    func testInterleavedStartsExactlyOneWins() {
        let c = ReaderCollect(amountMinor: 1000, currency: "gbp", key: "r-1", timeoutMs: 110_000, clientSecret: "pi_1_secret_x", locationId: "tml_9")
        var tillResults: [CollectResult] = []
        // both enter the coordinator's queue back to back
        coordinator.collect(CollectRequest(provider: "stripe", amountMinor: 1000, currency: "gbp", idempotencyKey: "own-1",
                                           description: nil, deadlineUnixMs: clock.now + 100_000)) { tillResults.append($0) }
        sq.sync { payments.collect(c, remainingMs: 110_000, started: { self.starts.append($0) }, end: { self.ends.append($0) }) }
        coordinator.flush(); sq.sync {}
        XCTAssertEqual(starts, [false], "the till took the lock first; the reader is told busy")
        XCTAssertTrue(tillResults.isEmpty, "the till's collect is still running")
    }

    func testAmountMismatchWithNoMoneyMovedIsReportedPending() {
        reader.retrieveResult = .success(FakeReader.intent(.requiresPaymentMethod, amount: 999))
        collect()
        XCTAssertEqual(ends, [.result(.pending(reason: "amount_mismatch"))])
        XCTAssertEqual(reader.collectCalls, 0, "nothing is collected on the wrong amount")
    }

    func testDeclinedCardIsReportedDeclinedWithItsCode() {
        collect()
        reader.confirmOutcomes = [.failed(intent: FakeReader.intent(.requiresPaymentMethod), declineCode: "card_declined")]
        reader.finishCollect(.success(FakeReader.intent(.requiresConfirmation)))
        coordinator.flush(); sq.sync {}; sq.sync {}
        XCTAssertEqual(ends, [.result(.declined(code: "card_declined"))])
    }

    func testLocationChangeReconnects() {
        collect()   // connects to tml_9
        reader.finishCollect(.failure(.canceledByReader))
        coordinator.flush(); sq.sync {}
        XCTAssertEqual(reader.connectCalls.map { $0.locationId }, ["tml_9"])
        let c = ReaderCollect(amountMinor: 1000, currency: "gbp", key: "k-2", timeoutMs: 110_000, clientSecret: "pi_2_secret_y", locationId: "tml_other")
        sq.sync { payments.collect(c, remainingMs: 110_000, started: { _ in }, end: { _ in }) }
        coordinator.flush(); sq.sync {}
        XCTAssertEqual(reader.disconnectCalls, 1)
        XCTAssertEqual(reader.connectCalls.map { $0.locationId }, ["tml_9", "tml_other"])
    }

    func testAKeyWithAnUnknownOutcomeIsNeverTappedAgain() {
        reader.retrieveResult = .success(FakeReader.intent(.processing))
        collect(key: "k-1")
        XCTAssertEqual(ends, [.result(.pending(reason: "outcome_unknown"))])
        reader.retrieveCalls = []
        collect(key: "k-1")
        XCTAssertEqual(ends.count, 2)
        XCTAssertEqual(ends.last, .result(.pending(reason: "outcome_unknown")))
        XCTAssertTrue(reader.retrieveCalls.isEmpty, "the same key is refused locally, nothing is retrieved or collected")
    }

    func testANewKeyDropsTheOldUnknownRecord() {
        reader.retrieveResult = .success(FakeReader.intent(.processing))
        collect(key: "k-1")
        reader.retrieveResult = .success(FakeReader.intent(.requiresPaymentMethod))
        collect(key: "k-2")
        XCTAssertEqual(reader.retrieveCalls.count, 2, "k-2 goes to the SDK: the main resolved k-1")
    }

    func testTheUnknownRecordExpiresAfterThirtyMinutes() {
        reader.retrieveResult = .success(FakeReader.intent(.processing))
        collect(key: "k-1")
        clock.now += CoordinatorReaderPayments.unknownTtlMs
        reader.retrieveCalls = []
        collect(key: "k-1")
        XCTAssertEqual(reader.retrieveCalls.count, 1)
    }

    func testCancelStopsTheCollect() {
        collect()
        sq.sync { payments.cancel(key: "k-1") }
        coordinator.flush(); sq.sync {}
        XCTAssertEqual(reader.cancelable.cancelCalls, 1)
    }

    func testDisconnectWhenIdleDropsTheReader() {
        reader.connected = true
        payments.disconnect()
        coordinator.flush()
        XCTAssertEqual(reader.disconnectCalls, 1)
    }

    func testDisconnectNeverHappensMidCollect() {
        collect()
        payments.disconnect()
        coordinator.flush()
        XCTAssertEqual(reader.disconnectCalls, 0)
    }
}

final class ReaderAmountTests: XCTestCase {
    private let us = Locale(identifier: "en_US")

    func testThousandMinorUnitsIsTenPounds() {
        XCTAssertEqual(ReaderAmount.format(amountMinor: 1000, currency: "gbp", locale: Locale(identifier: "en_GB")), "£10.00")
    }

    func testThreeDecimalCurrencyUsesItsOwnDigits() {
        XCTAssertTrue(ReaderAmount.format(amountMinor: 1234, currency: "KWD", locale: us).contains("1.234"))
    }

    func testZeroDecimalCurrency() {
        XCTAssertTrue(ReaderAmount.format(amountMinor: 500, currency: "JPY", locale: us).contains("500"))
        XCTAssertFalse(ReaderAmount.format(amountMinor: 500, currency: "JPY", locale: us).contains("."))
    }

    func testClock() {
        XCTAssertEqual(ReaderAmount.clock(seconds: 102), "1:42")
        XCTAssertEqual(ReaderAmount.clock(seconds: -5), "0:00")
    }
}
