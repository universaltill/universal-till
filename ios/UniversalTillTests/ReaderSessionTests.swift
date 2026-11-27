// The session state machine against the goldens: fake link, fake clock, scripted payments.
import XCTest

final class ReaderSessionTests: XCTestCase {
    var queue: DispatchQueue!
    var clock: FakeClock!
    var payments: FakeReaderPayments!
    var ui: FakeReaderUi!
    var link: FakeTransport!
    var session: ReaderSession!
    var gen = 0

    override func setUp() {
        super.setUp()
        queue = DispatchQueue(label: "test.reader.session")
        clock = FakeClock()
        payments = FakeReaderPayments()
        ui = FakeReaderUi()
        link = FakeTransport()
        session = ReaderSession(queue: queue, clock: clock, payments: payments, ui: ui, appVersion: "0.36.2")
    }

    func on(_ f: () -> Void) { queue.sync(execute: f) }
    func advance(_ ms: Int64) { clock.advance(to: clock.now + ms); queue.sync {}; queue.sync {} }

    func boot(foreground: Bool = true) {
        on {
            gen = session.attach(link)
            session.onText(gen: gen, Golden.text("hello_main"))
            if foreground { session.onForeground() }
        }
    }

    func collect(key: String = "att_7:1:1") {
        on { session.onText(gen: gen, Golden.text("collect").replacingOccurrences(of: "att_7:1:1", with: key)) }
    }

    // MARK: handshake

    func testSpeaksOnlyAfterTheMainsHello() {
        on { gen = session.attach(link) }
        XCTAssertTrue(link.sent.isEmpty)
        XCTAssertEqual(ui.last, .connecting)
        on { session.onText(gen: gen, Golden.text("collect")) }
        XCTAssertTrue(link.sent.isEmpty, "a collect before the hello is ignored")
        XCTAssertTrue(payments.collects.isEmpty)
    }

    func testHelloThenStatusAreSentAfterTheMainsHello() {
        boot()
        XCTAssertEqual(link.last("hello")?["payload"] as? [String: Any] != nil, true)
        XCTAssertEqual(link.payload("hello")?["role"] as? String, "card_reader")
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "ready")
        XCTAssertEqual(ui.last, .idle)
    }

    func testAHelloFromAnotherRoleClosesTheLink() {
        on {
            gen = session.attach(link)
            session.onText(gen: gen, #"{"v":1,"id":"m","type":"hello","payload":{"role":"satellite","reader_protocol":1}}"#)
        }
        XCTAssertTrue(link.closed)
        XCTAssertTrue(link.sent.isEmpty)
    }

    func testPingEveryFiveSeconds() {
        boot()
        XCTAssertEqual(link.frames("ping").count, 0)
        advance(5_000)
        XCTAssertEqual(link.frames("ping").count, 1)
        advance(5_000)
        XCTAssertEqual(link.frames("ping").count, 2)
    }

    func testNotReadyIsReportedAsNeedsSetup() {
        payments.cap = .of(.needsSetup, reason: "location_denied")
        boot()
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "needs_setup")
        XCTAssertEqual(link.payload("reader.status")?["reason"] as? String, "reader_needs_setup")
        XCTAssertEqual(ui.last, .needsSetup)
    }

    // MARK: collect

    func testCollectIsAckedThenStatusBusyThenPay() {
        boot()
        collect()
        let ack = link.frames("reader.collect").last
        XCTAssertEqual(ack?["reply_to"] as? String, "m1-3")
        XCTAssertTrue(jsonEqual(ack?["payload"], Golden.payload("collect_ack")))
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "busy")
        XCTAssertEqual(ui.last, .pay(amountMinor: 1250, currency: "gbp", timeoutMs: 110_000))
        XCTAssertEqual(ui.incomingCount, 1)
        XCTAssertEqual(payments.collects.first?.key, "att_7:1:1")
        XCTAssertEqual(payments.collects.first?.clientSecret, "pi_example_secret_example")
    }

    func testASecondCollectIsBusyAndTheFirstUntouched() {
        boot()
        collect(key: "k1")
        collect(key: "k2")
        let err = link.frames("error").last
        XCTAssertTrue(jsonEqual(err?["payload"], Golden.payload("collect_busy")))
        XCTAssertEqual(payments.collects.count, 1)
        XCTAssertTrue(payments.cancels.isEmpty)
        XCTAssertEqual(ui.refusedCount, 1)
    }

    func testApprovedResultGoesOnTheArrivalLink() {
        boot()
        collect()
        on { payments.finish(.result(.approved)) }
        let r = link.frames("reader.result").last
        XCTAssertTrue(jsonEqual(r?["payload"], Golden.payload("result_approved")))
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "ready")
        XCTAssertEqual(ui.last, .result(.done))
    }

    func testDeclinedAndPendingResultsMatchTheirGoldens() {
        boot()
        collect()
        on { payments.finish(.result(.declined(code: "card_declined"))) }
        XCTAssertTrue(jsonEqual(link.frames("reader.result").last?["payload"], Golden.payload("result_declined")))
        XCTAssertEqual(ui.last, .result(.cardRefused))
        collect()
        on { payments.finish(.result(.pending(reason: "amount_mismatch"))) }
        XCTAssertTrue(jsonEqual(link.frames("reader.result").last?["payload"], Golden.payload("result_pending")))
        XCTAssertEqual(ui.last, .result(.pending))   // "Ask the cashier", never "Time is up" for a maybe-charged sale
    }

    func testNotStartedIsReportedCancelled() {
        boot()
        collect()
        on { payments.finish(.notStarted) }
        XCTAssertEqual(link.payload("reader.result")?["status"] as? String, "cancelled")
    }

    func testResultIsDroppedWhenTheLinkChangedAndShownAsLostContact() {
        boot()
        collect()
        let second = FakeTransport()
        on {
            gen = session.attach(second)
            session.onText(gen: gen, Golden.text("hello_main"))
            payments.finish(.result(.approved))
        }
        XCTAssertTrue(second.frames("reader.result").isEmpty, "the verdict never travels on a link the collect did not arrive on")
        XCTAssertTrue(link.frames("reader.result").isEmpty)
        XCTAssertEqual(ui.last, .result(.linkLost))
    }

    func testCollectRefusedAsBusyByTheCoordinatorIsAnErrorFrame() {
        boot()
        payments.acceptStart = false
        collect()
        XCTAssertTrue(jsonEqual(link.frames("error").last?["payload"], Golden.payload("collect_busy")))
        XCTAssertTrue(link.frames("reader.collect").isEmpty, "no ack for a collect that was not started")
        XCTAssertFalse(session.hasLive)
        XCTAssertEqual(ui.refusedCount, 1)
    }

    func testCollectWhileNotReadyIsErrorNotReady() {
        boot()
        payments.cap = .of(.needsSetup)
        collect()
        XCTAssertEqual(link.payload("error")?["code"] as? String, "not_ready")
        XCTAssertTrue(payments.collects.isEmpty)
    }

    func testCollectWhileBackgroundedIsBusy() {
        boot(foreground: false)
        collect()
        XCTAssertEqual(link.payload("error")?["code"] as? String, "busy")
        XCTAssertTrue(payments.collects.isEmpty)
    }

    func testBadCollectIsABadMessage() {
        boot()
        on { session.onText(gen: gen, #"{"v":1,"id":"m9","type":"reader.collect","payload":{"provider":"stripe"}}"#) }
        XCTAssertEqual(link.payload("error")?["code"] as? String, "bad_message")
    }

    func testTillBusyIsReportedAndRefusesAReaderCollect() {
        boot()
        on { payments.onTillBusyChange?(true) }
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "busy")
        collect()
        XCTAssertEqual(link.payload("error")?["code"] as? String, "busy")
        XCTAssertEqual(ui.refusedCount, 1)
        on { payments.onTillBusyChange?(false) }
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "ready")
    }

    // MARK: cancel

    func testCancelForTheActiveKeyStopsIt() {
        boot()
        collect()
        on { session.onText(gen: gen, Golden.text("cancel")) }
        XCTAssertEqual(payments.cancels, ["att_7:1:1"])
    }

    func testCancelForAnotherKeyIsIgnored() {
        boot()
        collect(key: "other")
        on { session.onText(gen: gen, Golden.text("cancel")) }
        XCTAssertTrue(payments.cancels.isEmpty)
    }

    func testThePhonesOwnCancelStopsTheLiveCollect() {
        boot()
        collect()
        on { session.cancelLive() }
        XCTAssertEqual(payments.cancels, ["att_7:1:1"])
    }

    // MARK: link loss

    func testLossOfThirtySecondsCancelsTheCollect() {
        boot()
        collect()
        on { session.onClosed(gen: gen, authFailed: false) }
        advance(29_000)
        XCTAssertTrue(payments.cancels.isEmpty)
        advance(1_000)
        XCTAssertEqual(payments.cancels, ["att_7:1:1"])
        XCTAssertEqual(ui.last, .result(.linkLost))
    }

    func testAReconnectAttemptDoesNotRestartTheLossClock() {
        boot()
        collect()
        on { session.onClosed(gen: gen, authFailed: false) }
        advance(20_000)
        let second = FakeTransport()
        on { gen = session.attach(second) }
        on { session.onClosed(gen: gen, authFailed: false) }
        advance(10_000)
        XCTAssertEqual(payments.cancels, ["att_7:1:1"], "anchored to the first loss")
    }

    func testContactWithTheMainEndsTheLossClock() {
        boot()
        collect()
        on { session.onClosed(gen: gen, authFailed: false) }
        advance(10_000)
        on {
            gen = session.attach(FakeTransport())
            session.onText(gen: gen, Golden.text("hello_main"))
        }
        advance(60_000)
        XCTAssertTrue(payments.cancels.isEmpty)
    }

    func testIdleLossShowsOffline() {
        boot()
        on { session.onClosed(gen: gen, authFailed: false) }
        XCTAssertEqual(ui.last, .offline)
    }

    func testAStaleLinkCloseIsIgnored() {
        boot()
        let old = gen
        on { gen = session.attach(FakeTransport()) }
        on { session.onClosed(gen: old, authFailed: true) }
        XCTAssertFalse(session.isRevoked)
    }

    // MARK: revoke

    func testAuthFailureIsRevokedAndCancelsAndDisconnects() {
        boot()
        collect()
        on { session.onClosed(gen: gen, authFailed: true) }
        XCTAssertTrue(session.isRevoked)
        XCTAssertEqual(ui.revokedCount, 1)
        XCTAssertEqual(ui.last, .revoked)
        XCTAssertEqual(payments.cancels, ["att_7:1:1"])
        XCTAssertEqual(payments.disconnects, 1)
        on { session.onText(gen: gen, Golden.text("collect")) }
        XCTAssertEqual(payments.collects.count, 1, "nothing is accepted after a revoke")
    }

    func testCloseCode4003AndHttp401AreRevokedOthersAreNot() {
        XCTAssertTrue(ReaderLinkClose.isRevoked(closeCode: 4003))
        XCTAssertFalse(ReaderLinkClose.isRevoked(closeCode: 1006))
        XCTAssertTrue(ReaderLinkClose.isRevoked(httpStatus: 401))
        for s in [403, 429, 503, 500] { XCTAssertFalse(ReaderLinkClose.isRevoked(httpStatus: s)) }
    }

    func testBackOffIsOneToFifteenSecondsOrRetryAfter() {
        XCTAssertEqual((0...6).map { ReaderLinkClose.retryDelayMs(attempt: $0, retryAfterSec: nil) },
                       [1_000, 2_000, 4_000, 8_000, 15_000, 15_000, 15_000])
        XCTAssertEqual(ReaderLinkClose.retryDelayMs(attempt: 0, retryAfterSec: 7), 7_000)
        XCTAssertEqual(ReaderLinkClose.retryDelayMs(attempt: 0, retryAfterSec: 9_999), 120_000)
    }

    func testEnrolledClearsTheRevokedState() {
        boot()
        on { session.onClosed(gen: gen, authFailed: true) }
        on { session.enrolled() }
        XCTAssertFalse(session.isRevoked)
    }

    // MARK: token

    func testTokenRequestIsAnEmptyPayloadAndTheReplyIsDelivered() {
        boot()
        var got: TokenResult?
        on { session.fetchToken { got = $0 } }
        let req = link.frames("reader.token").last
        XCTAssertTrue(jsonEqual(req?["payload"], Golden.payload("token_request")))
        let id = req?["id"] as? String ?? ""
        on { session.onText(gen: gen, Golden.text("token_reply").replacingOccurrences(of: "p1-4", with: id)) }
        XCTAssertEqual(got, .ok(secret: "pst_test_example_secret", locationId: "tml_example"))
    }

    func testTokenTimesOutAfterTenSeconds() {
        boot()
        var got: TokenResult?
        on { session.fetchToken { got = $0 } }
        advance(9_999)
        XCTAssertNil(got)
        advance(1)
        XCTAssertEqual(got, .err("timeout"))
    }

    func testTokenErrorReplyCarriesItsCode() {
        boot()
        var got: TokenResult?
        on { session.fetchToken { got = $0 } }
        let id = link.frames("reader.token").last?["id"] as? String ?? ""
        on { session.onText(gen: gen, #"{"v":1,"id":"m5","type":"error","reply_to":"\#(id)","payload":{"code":"rate_limited","retryable":true}}"#) }
        XCTAssertEqual(got, .err("rate_limited"))
    }

    func testTokenWithoutALinkIsOffline() {
        var got: TokenResult?
        on { session.fetchToken { got = $0 } }
        XCTAssertEqual(got, .err("offline"))
    }

    func testAPendingTokenFailsWhenTheLinkIsReplaced() {
        boot()
        var got: TokenResult?
        on { session.fetchToken { got = $0 } }
        on { _ = session.attach(FakeTransport()) }
        XCTAssertEqual(got, .err("offline"))
    }

    func testASecondTokenReplyForTheSameIdIsIgnored() {
        boot()
        var calls = 0
        on { session.fetchToken { _ in calls += 1 } }
        let id = link.frames("reader.token").last?["id"] as? String ?? ""
        let reply = Golden.text("token_reply").replacingOccurrences(of: "p1-4", with: id)
        on { session.onText(gen: gen, reply); session.onText(gen: gen, reply) }
        XCTAssertEqual(calls, 1)
    }

    // MARK: lifecycle

    func testBackgroundWhileIdleDisconnectsAndReportsUnavailable() {
        boot()
        on { session.onBackground() }
        XCTAssertEqual(payments.disconnects, 1)
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "unavailable")
        XCTAssertEqual(ui.last, .paused)
    }

    func testBackgroundMidCollectDoesNotCancelOrDisconnect() {
        boot()
        collect()
        on { session.onBackground() }
        XCTAssertTrue(payments.cancels.isEmpty)
        XCTAssertEqual(payments.disconnects, 0)
        XCTAssertTrue(session.hasLive)
    }

    func testForegroundAfterBackgroundReportsReadyAgain() {
        boot()
        on { session.onBackground() }
        on { session.onForeground() }
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "ready")
    }

    func testResultCardAutoDismissesToIdleAfterFourSeconds() {
        boot()
        collect()
        on { payments.finish(.result(.approved)) }
        XCTAssertEqual(ui.last, .result(.done))
        advance(4_000)
        XCTAssertEqual(ui.last, .idle)
    }

    func testSetupReReportsStatus() {
        payments.cap = .of(.needsSetup)
        boot()
        on { session.setup() }
        XCTAssertEqual(payments.setups, 1)
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "ready")
        XCTAssertEqual(ui.last, .idle)
    }
}
