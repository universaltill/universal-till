// ut-docs#6538 (S6): the main's reader.cancel through the REAL session, payments
// adapter and coordinator, on the fake Stripe reader. Whatever the SDK does (or
// never does), the phone ends the collect, answers reader.result cancelled and
// reports ready again; a cancel for another key does nothing.
import XCTest

final class ReaderCancelFlowTests: XCTestCase {
    var reader: FakeReader!
    var clock: FakeClock!
    var coordinator: TapToPayCoordinator!
    var payments: CoordinatorReaderPayments!
    var session: ReaderSession!
    var ui: FakeReaderUi!
    var link: FakeTransport!
    var gen = 0
    let sq = DispatchQueue(label: "test.reader.cancelflow")
    let key = "att_7:1:1"

    override func setUp() {
        super.setUp()
        reader = FakeReader()
        reader.connected = true
        reader.collectResult = nil   // the phone waits for the card
        reader.retrieveResult = .success(FakeReader.intent(.requiresPaymentMethod, amount: 1250))
        clock = FakeClock()
        let store = FakeStore()
        store.markSetupDone("stripe")
        coordinator = TapToPayCoordinator(reader: reader, cloud: FakeCloud(), clock: clock, location: FakeLocation(),
                                          education: FakeEducation(), store: store, runBlocking: { $0() })
        coordinator.flush()
        payments = CoordinatorReaderPayments(coordinator: coordinator, clock: clock, sessionQueue: sq)
        ui = FakeReaderUi()
        link = FakeTransport()
        session = ReaderSession(queue: sq, clock: clock, payments: payments, ui: ui, appVersion: "0.36.2")
        sq.sync {
            gen = session.attach(link)
            session.onText(gen: gen, Golden.text("hello_main"))
            session.onForeground()
        }
        settle()
    }

    func settle() { coordinator.flush(); sq.sync {}; coordinator.flush(); sq.sync {}; sq.sync {} }

    func startCollect() {
        sq.sync { session.onText(gen: gen, Golden.text("collect")) }
        settle()
        XCTAssertEqual(reader.collectCalls, 1, "the collect is running on the SDK")
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "busy")
    }

    func sendCancel(_ k: String? = nil) {
        let text = Golden.text("cancel").replacingOccurrences(of: "att_7:1:1", with: k ?? key)
        sq.sync { session.onText(gen: gen, text) }
        settle()
    }

    func resultState() -> String? { link.payload("reader.result")?["status"] as? String }

    func testCancelForTheLiveKeyEndsTheCollectCancelledAndReportsReadyAgain() {
        reader.cancelable.onCancel = { [weak self] in self?.reader.finishCollect(.failure(.canceledByUs)) }
        startCollect()
        sendCancel()
        XCTAssertEqual(reader.cancelable.cancelCalls, 1, "the Stripe collect was cancelled")
        XCTAssertEqual(link.frames("reader.result").count, 1)
        XCTAssertEqual(resultState(), "cancelled")
        XCTAssertEqual(link.payload("reader.result")?["idempotency_key"] as? String, key)
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "ready")
        XCTAssertEqual(ui.last, .result(.cancelled))
        XCTAssertEqual(reader.confirmCalls, 0, "a cancelled collect is never confirmed")
        XCTAssertFalse(session.hasLive)
    }

    func testCancelWhileAppleSheetMakesTheAppInactiveStillCancels() {
        reader.cancelable.onCancel = { [weak self] in self?.reader.finishCollect(.failure(.canceledByUs)) }
        startCollect()
        sq.sync { session.onBackground() }   // Apple's sheet: the app is no longer foreground
        settle()
        XCTAssertEqual(reader.disconnectCalls, 0)
        sendCancel()
        XCTAssertEqual(resultState(), "cancelled")
        XCTAssertEqual(reader.cancelable.cancelCalls, 1)
    }

    func testCancelForAnUnknownKeyIsIgnored() {
        startCollect()
        let before = link.sent.count
        sendCancel("att_9:9:9")
        XCTAssertEqual(reader.cancelable.cancelCalls, 0)
        XCTAssertTrue(link.frames("reader.result").isEmpty)
        XCTAssertEqual(link.sent.count, before, "nothing is sent for a foreign key")
        XCTAssertTrue(session.hasLive, "the collect is still live")
    }

    func testCancelWithNoLiveCollectIsIgnored() {
        sendCancel()
        XCTAssertEqual(reader.cancelable.cancelCalls, 0)
        XCTAssertTrue(link.frames("reader.result").isEmpty)
    }

    func testCancelWhileThePaymentIntentIsBeingReadEndsCancelled() {
        reader.holdRetrieve = true
        sq.sync { session.onText(gen: gen, Golden.text("collect")) }
        settle()
        XCTAssertEqual(reader.collectCalls, 0)
        sendCancel()
        reader.finishRetrieve(.success(FakeReader.intent(.requiresPaymentMethod, amount: 1250)))
        settle()
        XCTAssertEqual(reader.collectCalls, 0, "the tap screen never opens")
        XCTAssertEqual(resultState(), "cancelled")
    }

    func testCancelWhoseSdkCallbackNeverFiresStillEndsCancelledWithinTheBound() {
        reader.cancelable.holdDone = true   // the SDK's cancel completion never arrives
        startCollect()
        sendCancel()
        XCTAssertEqual(reader.cancelable.cancelCalls, 1)
        XCTAssertTrue(link.frames("reader.result").isEmpty, "not yet: the SDK may still answer")
        clock.advance(to: clock.now + TapToPayCoordinator.cancelGraceMs)
        settle()
        XCTAssertEqual(resultState(), "cancelled")
        XCTAssertEqual(link.payload("reader.status")?["state"] as? String, "ready")
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    func testCancelWhoseCollectNeverReturnsStillEndsCancelledWithinTheBound() {
        startCollect()           // cancel completes, but the SDK never ends the collect
        sendCancel()
        XCTAssertTrue(link.frames("reader.result").isEmpty)
        clock.advance(to: clock.now + TapToPayCoordinator.cancelGraceMs)
        settle()
        XCTAssertEqual(resultState(), "cancelled")
        XCTAssertEqual(reader.confirmCalls, 0)
    }

    func testALateCollectAfterTheBoundIsNeverConfirmed() {
        reader.cancelable.holdDone = true
        startCollect()
        sendCancel()
        clock.advance(to: clock.now + TapToPayCoordinator.cancelGraceMs)
        settle()
        reader.finishCollect(.success(FakeReader.intent(.requiresConfirmation)))
        settle()
        XCTAssertEqual(reader.confirmCalls, 0)
        XCTAssertEqual(link.frames("reader.result").count, 1, "exactly one verdict")
    }

    func testACancelAfterTheTapWasTakenForConfirmDoesNotTurnItIntoCancelled() {
        reader.holdConfirm = true
        startCollect()
        reader.finishCollect(.success(FakeReader.intent(.requiresConfirmation)))
        settle()
        sendCancel()
        clock.advance(to: clock.now + TapToPayCoordinator.cancelGraceMs)
        settle()
        XCTAssertTrue(link.frames("reader.result").isEmpty, "confirm cannot be cancelled: no verdict yet, never declined")
        reader.finishConfirm(.succeeded(FakeReader.intent(.succeeded, amount: 1250, charge: true)))
        settle()
        XCTAssertEqual(resultState(), "approved")
    }
}
