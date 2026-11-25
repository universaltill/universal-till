// Fakes for the Tap to Pay coordinator tests (ut-docs#3049): a scripted
// reader, cloud, clock, location, education and setup store. Compiled into
// the host-less UniversalTillTapToPayTests bundle together with
// UniversalTill/TapToPay/Core/ (same module, so no @testable import).
import Foundation

final class FakeCancelable: ReaderCancelable {
    var cancelCalls = 0
    var pendingDone: ((Bool) -> Void)?
    /// Called when cancel() is invoked (e.g. to make the reader fail the
    /// collect with .canceledByUs).
    var onCancel: (() -> Void)?
    /// When true, cancel's completion is held until releaseCancel().
    var holdDone = false

    func cancel(_ done: @escaping (Bool) -> Void) {
        cancelCalls += 1
        onCancel?()
        if holdDone { pendingDone = done } else { done(true) }
    }

    func releaseCancel(_ ok: Bool = true) {
        let d = pendingDone
        pendingDone = nil
        d?(ok)
    }
}

final class FakeReader: PaymentReader {
    var supportResult: SupportCheck = .supported
    var connected = false
    var isConnected: Bool { connected }

    // connect
    var connectCalls: [(locationId: String, allowTerms: Bool)] = []
    var connectError: ReaderError?
    var holdConnect = false
    var pendingConnect: ((ReaderError?) -> Void)?

    // retrieve
    var retrieveCalls: [String] = []
    var retrieveResult: Result<ReaderIntent, ReaderError> = .success(FakeReader.intent(.requiresPaymentMethod))

    // collect
    var collectCalls = 0
    var collectResult: Result<ReaderIntent, ReaderError>? = .success(FakeReader.intent(.requiresConfirmation))
    var pendingCollect: ((Result<ReaderIntent, ReaderError>) -> Void)?
    let cancelable = FakeCancelable()

    // confirm
    var confirmCalls = 0
    var confirmOutcomes: [ConfirmOutcome] = [.succeeded(FakeReader.intent(.succeeded, charge: true))]
    var holdConfirm = false
    var pendingConfirm: ((ConfirmOutcome) -> Void)?

    static func intent(_ status: IntentStatus, amount: Int64 = 1000, currency: String = "gbp",
                       charge: Bool = false) -> ReaderIntent {
        ReaderIntent(id: "pi_1", status: status, amountMinor: amount, currency: currency,
                     chargeId: charge ? "ch_1" : nil, brand: charge ? "visa" : nil,
                     last4: charge ? "4242" : nil, opaque: nil)
    }

    func support() -> SupportCheck { supportResult }

    func connect(locationId: String, allowTermsAcceptance: Bool,
                 progress: @escaping (Int) -> Void, completion: @escaping (ReaderError?) -> Void) {
        connectCalls.append((locationId, allowTermsAcceptance))
        progress(40)
        let finish: (ReaderError?) -> Void = { [weak self] err in
            if err == nil { self?.connected = true }
            completion(err)
        }
        if holdConnect { pendingConnect = finish } else { finish(connectError) }
    }

    func releaseConnect(_ err: ReaderError? = nil) {
        let c = pendingConnect
        pendingConnect = nil
        c?(err)
    }

    func retrieveIntent(clientSecret: String, completion: @escaping (Result<ReaderIntent, ReaderError>) -> Void) {
        retrieveCalls.append(clientSecret)
        completion(retrieveResult)
    }

    func collect(_ intent: ReaderIntent, completion: @escaping (Result<ReaderIntent, ReaderError>) -> Void) -> ReaderCancelable {
        collectCalls += 1
        if let r = collectResult {
            completion(r)
        } else {
            pendingCollect = completion
        }
        return cancelable
    }

    var disconnectCalls = 0
    func disconnect(completion: @escaping () -> Void) {
        disconnectCalls += 1
        connected = false
        completion()
    }

    func finishCollect(_ r: Result<ReaderIntent, ReaderError>) {
        let c = pendingCollect
        pendingCollect = nil
        c?(r)
    }

    func confirm(_ intent: ReaderIntent, completion: @escaping (ConfirmOutcome) -> Void) {
        confirmCalls += 1
        if holdConfirm {
            pendingConfirm = completion
            return
        }
        let outcome = confirmOutcomes.isEmpty ? .failed(intent: nil, declineCode: nil) : confirmOutcomes.removeFirst()
        completion(outcome)
    }

    func finishConfirm(_ o: ConfirmOutcome) {
        let c = pendingConfirm
        pendingConfirm = nil
        c?(o)
    }
}

final class FakeCloud: TapToPayCloudClient {
    var responses: [String: Result<[String: Any], CloudError>] = [
        "onboarding_status": .success(["onboarded": true, "charges_enabled": true, "location_id": "tml_1"]),
        "session_create": .success(["id": "pi_1", "client_secret": "pi_1_secret_x", "status": "requires_payment_method",
                                    "amount_minor": 1000, "currency": "gbp"]),
    ]
    var calls: [(op: String, body: [String: Any])] = []

    func call(_ op: String, _ body: [String: Any]) throws -> [String: Any] {
        calls.append((op, body))
        guard let r = responses[op] else { throw CloudError(code: "op_not_allowed") }
        switch r {
        case .success(let v): return v
        case .failure(let e): throw e
        }
    }

    func count(_ op: String) -> Int { calls.filter { $0.op == op }.count }
}

final class FakeClock: TapToPayClock {
    private final class Timer: TimerToken {
        let at: Int64
        let queue: DispatchQueue
        let block: () -> Void
        var live = true
        init(at: Int64, queue: DispatchQueue, block: @escaping () -> Void) {
            self.at = at; self.queue = queue; self.block = block
        }
        func invalidate() { live = false }
    }

    var now: Int64 = 1_763_300_000_000
    private var timers: [Timer] = []
    private let lock = NSLock()

    func nowMs() -> Int64 { lock.lock(); defer { lock.unlock() }; return now }

    func schedule(atMs: Int64, on queue: DispatchQueue, _ block: @escaping () -> Void) -> TimerToken {
        let t = Timer(at: atMs, queue: queue, block: block)
        lock.lock(); timers.append(t); lock.unlock()
        return t
    }

    /// Moves time to `ms` and fires every live timer now due, on its queue.
    func advance(to ms: Int64) {
        lock.lock()
        now = ms
        let due = timers.filter { $0.live && $0.at <= ms }
        timers.removeAll { t in due.contains(where: { $0 === t }) }
        lock.unlock()
        for t in due { t.queue.async { if t.live { t.block() } } }
    }
}

final class FakeLocation: LocationAuthorizer {
    var granted = true
    var calls = 0
    func ensureWhenInUse(_ done: @escaping (Bool) -> Void) { calls += 1; done(granted) }
}

final class FakeEducation: EducationPresenter {
    var presented = 0
    var reason: String?
    func presentHowToTap(_ done: @escaping (String?) -> Void) { presented += 1; done(reason) }
}

final class FakeStore: SetupStore {
    var done: Set<String> = []
    func isSetupDone(_ provider: String) -> Bool { done.contains(provider) }
    func markSetupDone(_ provider: String) { done.insert(provider) }
}
