// The Tap to Pay on iPhone state machine (ut-docs#3049, design §4.3,
// ADR-0125 §3). One payment at a time, on one serial queue; the provider
// SDK behind PaymentReader, the cloud broker behind TapToPayCloudClient,
// time behind TapToPayClock — all injected, so every branch is unit-tested
// (UniversalTillTests/TapToPayCoordinatorTests.swift).
//
// Invariants:
//  - A collect is never left running: a cancel (operator or deadline) waits
//    for both the cancel's own completion and the collect's before the
//    verdict is returned.
//  - Unknown is pending, never declined (#6439). A deadline after the
//    PaymentIntent was asked for, or during confirm, is pending/timeout;
//    the plugin settles it through session_status (#6456).
//  - A PaymentIntent that already succeeded is approved with no tap.
//  - The cashier's path (collect, prewarm) never shows Apple's terms; only
//    the manager's setup() connects with terms acceptance allowed.
import Foundation

final class TapToPayCoordinator {
    /// The verdict is due this long before the caller's hard deadline, so it
    /// reaches Go before Go's context ends (Go then cancels and says pending).
    static let softDeadlineMarginMs: Int64 = 3_000
    /// A requires_confirmation confirm is retried once only if this much
    /// time is left before the soft deadline.
    static let confirmRetryFloorMs: Int64 = 5_000

    let provider: String
    private let reader: PaymentReader
    private let cloud: TapToPayCloudClient
    private let clock: TapToPayClock
    private let location: LocationAuthorizer
    private let education: EducationPresenter
    private let store: SetupStore
    private let runBlocking: (@escaping () -> Void) -> Void
    private let queue: DispatchQueue

    /// Called on the coordinator's queue whenever the capability changes;
    /// the Bool is "the manager's setup just failed" (for the banner).
    /// Queue-confined: set through observeStatus only.
    private var onStatusChange: ((Capability, Bool) -> Void)?

    /// Installs the status observer and tells it the current capability.
    /// Goes through the queue, because init's own first refresh runs there
    /// and would otherwise race with the assignment.
    func observeStatus(_ handler: @escaping (Capability, Bool) -> Void) {
        queue.async {
            self.onStatusChange = handler
            self.refreshCapability()
        }
    }

    // Capability, readable from any thread without touching the queue.
    private let capLock = NSLock()
    private var cap: Capability = .of(.needsSetup)

    // Queue-confined state.
    private var current: Attempt?
    private var setupInFlight = false
    private var locationId: String?
    private var connecting = false
    private var connectProgress = 0
    private var connectWaiters: [(allowTerms: Bool, done: (ReaderError?) -> Void)] = []
    private var lastConnectError: ReaderError?
    private var setupFailed = false

    init(provider: String = "stripe", reader: PaymentReader, cloud: TapToPayCloudClient, clock: TapToPayClock,
         location: LocationAuthorizer, education: EducationPresenter, store: SetupStore,
         runBlocking: @escaping (@escaping () -> Void) -> Void = { DispatchQueue.global(qos: .userInitiated).async(execute: $0) },
         queue: DispatchQueue = DispatchQueue(label: "com.universaltill.pos.taptopay")) {
        self.provider = provider
        self.reader = reader
        self.cloud = cloud
        self.clock = clock
        self.location = location
        self.education = education
        self.store = store
        self.runBlocking = runBlocking
        self.queue = queue
        queue.async { self.refreshCapability() }
    }

    // MARK: capability

    func capability() -> Capability {
        capLock.lock(); defer { capLock.unlock() }
        return cap
    }

    /// {"tap_to_pay":{"<provider>":Capability}} — devicepay.Capabilities.
    func capabilitiesJSON() -> String {
        let c = capability()
        guard let data = try? JSONEncoder().encode(["tap_to_pay": [provider: c]]),
              let s = String(data: data, encoding: .utf8) else { return "{}" }
        return s
    }

    /// Recomputes the capability from the queue-confined state.
    private func refreshCapability() {
        let next: Capability
        if case .unsupported(let reason) = reader.support() {
            next = .of(.unavailable, reason: reason)
        } else if reader.isConnected {
            next = .of(.ready)
        } else if connecting {
            next = .of(.preparing, progress: connectProgress)
        } else if let err = lastConnectError {
            switch err {
            case .termsNotAccepted, .locationDenied:
                next = .of(.needsSetup, reason: err.reason)
            default:
                next = .of(.unavailable, reason: err.reason)
            }
        } else if let reason = lastCloudReason {
            next = .of(.unavailable, reason: reason)
        } else if !store.isSetupDone(provider) {
            next = .of(.needsSetup)
        } else {
            next = .of(.preparing, progress: 0)
        }
        capLock.lock()
        cap = next
        capLock.unlock()
        onStatusChange?(next, setupFailed)
    }

    private var lastCloudReason: String?

    // MARK: collect

    private enum Phase { case connecting, creating, collecting, confirming }

    private final class Attempt {
        let req: CollectRequest
        let completion: (CollectResult) -> Void
        var phase: Phase = .connecting
        var finished = false
        var timer: TimerToken?
        var cancelable: ReaderCancelable?
        var cancelReason: String?          // timeout | operator, once a cancel was asked for
        var cancelInFlight = false
        var collectOutcome: Result<ReaderIntent, ReaderError>?
        var confirmRetried = false
        var cloudMismatch = false          // session_create answered another amount or currency

        init(_ req: CollectRequest, _ completion: @escaping (CollectResult) -> Void) {
            self.req = req
            self.completion = completion
        }
    }

    private func softDeadline(_ a: Attempt) -> Int64 { a.req.deadlineUnixMs - Self.softDeadlineMarginMs }

    func collect(_ req: CollectRequest, completion: @escaping (CollectResult) -> Void) {
        queue.async { self.startCollect(req, completion) }
    }

    private func startCollect(_ req: CollectRequest, _ completion: @escaping (CollectResult) -> Void) {
        if current != nil || setupInFlight {
            completion(.declined(TapToPayReason.busy))
            return
        }
        if req.provider != provider {
            completion(.declined(TapToPayReason.unsupportedProvider))
            return
        }
        if req.amountMinor <= 0 || req.currency.count != 3 || req.idempotencyKey.isEmpty {
            completion(.declined(TapToPayReason.badRequest))
            return
        }
        if case .unsupported(let reason) = reader.support() {
            completion(.declined(reason))
            return
        }
        let a = Attempt(req, completion)
        current = a
        a.timer = clock.schedule(atMs: softDeadline(a), on: queue) { [weak self, weak a] in
            guard let self, let a else { return }
            self.deadlinePassed(a)
        }
        ensureConnected(allowTerms: false) { [weak self] err in
            guard let self, self.live(a) else { return }
            if let err {
                self.finish(a, .declined(err.reason))
                return
            }
            self.createIntent(a)
        }
    }

    private func live(_ a: Attempt) -> Bool { current === a && !a.finished }

    private func finish(_ a: Attempt, _ result: CollectResult) {
        guard !a.finished else { return }
        a.finished = true
        a.timer?.invalidate()
        if current === a { current = nil }
        a.completion(result)
    }

    private func createIntent(_ a: Attempt) {
        a.phase = .creating
        var body: [String: Any] = [
            "amount_minor": a.req.amountMinor,
            "currency": a.req.currency,
            "idempotency_key": a.req.idempotencyKey,
        ]
        if let d = a.req.description { body["description"] = d }
        cloudCall("session_create", body) { [weak self] result in
            guard let self, self.live(a) else { return }
            switch result {
            case .failure(let err):
                // No PaymentIntent exists, or a retry with this key gets the same one.
                self.finish(a, .declined(TapToPayReason.fromCloud(err.code)))
            case .success(let pi):
                // A mismatch is decided in retrieved(), once the intent's
                // status is known: it is only a decline if no money moved.
                if let amount = (pi["amount_minor"] as? NSNumber)?.int64Value, amount != a.req.amountMinor {
                    a.cloudMismatch = true
                }
                if let cur = pi["currency"] as? String, cur.lowercased() != a.req.currency.lowercased() {
                    a.cloudMismatch = true
                }
                guard let secret = pi["client_secret"] as? String, !secret.isEmpty else {
                    self.finish(a, .declined(TapToPayReason.other))
                    return
                }
                self.reader.retrieveIntent(clientSecret: secret) { r in
                    self.queue.async { self.retrieved(a, r) }
                }
            }
        }
    }

    private func retrieved(_ a: Attempt, _ r: Result<ReaderIntent, ReaderError>) {
        guard live(a) else { return }
        guard case .success(let intent) = r else {
            // The PaymentIntent exists but its state is unknown here.
            finish(a, .pending(TapToPayReason.outcomeUnknown))
            return
        }
        if a.cloudMismatch || intent.amountMinor != a.req.amountMinor
            || intent.currency.lowercased() != a.req.currency.lowercased() {
            switch intent.status {
            case .requiresPaymentMethod, .requiresConfirmation, .canceled:
                // No money moved on this intent, and nothing is collected.
                finish(a, .declined(TapToPayReason.amountMismatch))
            case .succeeded, .processing, .requiresCapture, .unknown:
                // Money moved (or may have) on this key for another amount.
                // A decline would invite a second charge; the plugin settles
                // it through session_status (#6439).
                finish(a, .pending(TapToPayReason.amountMismatch))
            }
            return
        }
        switch intent.status {
        case .succeeded:
            finish(a, approved(intent))
        case .canceled:
            finish(a, .declined(TapToPayReason.intentCanceled))
        case .requiresConfirmation:
            if let reason = a.cancelReason {
                finish(a, .cancelled(reason))
            } else {
                confirm(a, intent)
            }
        case .requiresPaymentMethod:
            if let reason = a.cancelReason {
                finish(a, .cancelled(reason))
            } else {
                startCollectPhase(a, intent)
            }
        case .processing, .requiresCapture, .unknown:
            // The provider already has this payment in hand: no second tap
            // and no second confirm.
            finish(a, .pending(TapToPayReason.outcomeUnknown))
        }
    }

    private func startCollectPhase(_ a: Attempt, _ intent: ReaderIntent) {
        a.phase = .collecting
        a.cancelable = reader.collect(intent) { [weak self] r in
            guard let self else { return }
            self.queue.async {
                a.collectOutcome = r
                self.maybeFinishCollect(a)
            }
        }
    }

    private func requestCancel(_ a: Attempt, reason: String) {
        guard a.cancelReason == nil else { return }
        a.cancelReason = reason
        guard let c = a.cancelable else { return }
        a.cancelInFlight = true
        c.cancel { [weak self] _ in
            guard let self else { return }
            self.queue.async {
                a.cancelInFlight = false
                self.maybeFinishCollect(a)
            }
        }
    }

    /// Returns the collect's verdict only once the collect itself has
    /// completed and any cancel we asked for has completed too.
    private func maybeFinishCollect(_ a: Attempt) {
        guard live(a), let outcome = a.collectOutcome, !a.cancelInFlight else { return }
        switch outcome {
        case .success(let intent):
            if let reason = a.cancelReason {
                // Collected after we asked to stop: never confirmed, so no money moves.
                finish(a, .cancelled(reason))
            } else {
                confirm(a, intent)
            }
        case .failure(.canceledByReader):
            finish(a, .cancelled(TapToPayReason.customer))
        case .failure(let err):
            if let reason = a.cancelReason {
                finish(a, .cancelled(reason))
            } else if err == .canceledByUs {
                finish(a, .cancelled(TapToPayReason.operator))
            } else {
                // No authorisation was attempted.
                finish(a, .declined(TapToPayReason.readFailed))
            }
        }
    }

    private func confirm(_ a: Attempt, _ intent: ReaderIntent) {
        a.phase = .confirming
        reader.confirm(intent) { [weak self] outcome in
            guard let self else { return }
            self.queue.async { self.confirmed(a, outcome) }
        }
    }

    private func confirmed(_ a: Attempt, _ outcome: ConfirmOutcome) {
        guard !a.finished else {
            // The verdict already went back as pending/timeout; the plugin
            // settles through session_status. Never logs card data.
            NSLog("TapToPay: a confirm finished after its deadline (%@)", Self.describe(outcome))
            return
        }
        switch outcome {
        case .succeeded(let intent):
            finish(a, intent.status == .succeeded ? approved(intent) : .pending(TapToPayReason.outcomeUnknown))
        case .failed(nil, _):
            finish(a, .pending(TapToPayReason.outcomeUnknown))
        case .failed(let intent?, let code):
            switch intent.status {
            case .requiresPaymentMethod:
                finish(a, CollectResult(status: .declined, declineCode: code))
            case .requiresConfirmation:
                let left = softDeadline(a) - clock.nowMs()
                if !a.confirmRetried && left > Self.confirmRetryFloorMs {
                    a.confirmRetried = true
                    confirm(a, intent)
                } else {
                    finish(a, .pending(TapToPayReason.outcomeUnknown))
                }
            case .succeeded:
                finish(a, approved(intent))
            case .canceled:
                finish(a, .declined(TapToPayReason.intentCanceled))
            default:
                finish(a, .pending(TapToPayReason.outcomeUnknown))
            }
        }
    }

    private static func describe(_ o: ConfirmOutcome) -> String {
        switch o {
        case .succeeded(let i): return "succeeded \(i.id)"
        case .failed(let i, let code): return "failed \(i?.id ?? "unknown") \(code ?? "")"
        }
    }

    private func approved(_ intent: ReaderIntent) -> CollectResult {
        CollectResult(status: .approved, providerTxnId: intent.id, chargeId: intent.chargeId,
                      cardBrand: intent.brand, last4: intent.last4)
    }

    private func deadlinePassed(_ a: Attempt) {
        guard live(a) else { return }
        switch a.phase {
        case .connecting:
            // Nothing was asked of the PaymentIntent in this attempt.
            finish(a, .cancelled(TapToPayReason.timeout))
        case .creating:
            finish(a, .pending(TapToPayReason.timeout))
        case .collecting:
            requestCancel(a, reason: TapToPayReason.timeout)
        case .confirming:
            // Confirm can't be cancelled; it finishes in the background.
            finish(a, .pending(TapToPayReason.timeout))
        }
    }

    /// Cancels the in-flight collect with this key; otherwise a no-op.
    func cancel(idempotencyKey: String) {
        queue.async {
            guard let a = self.current, a.req.idempotencyKey == idempotencyKey, !a.finished else { return }
            switch a.phase {
            case .connecting:
                self.finish(a, .cancelled(TapToPayReason.operator))
            case .creating:
                // Decided once the PaymentIntent is read: it may have succeeded already.
                a.cancelReason = TapToPayReason.operator
            case .collecting:
                self.requestCancel(a, reason: TapToPayReason.operator)
            case .confirming:
                break // can't be cancelled
            }
        }
    }

    // MARK: connect

    /// Connects (or joins a connect already in flight). A waiter that allows
    /// terms acceptance, behind an in-flight connect that did not and failed
    /// on the terms, gets a fresh connect with acceptance allowed.
    private func ensureConnected(allowTerms: Bool, done: @escaping (ReaderError?) -> Void) {
        if reader.isConnected {
            done(nil)
            return
        }
        connectWaiters.append((allowTerms, done))
        if connecting { return }
        startConnect(allowTerms: allowTerms)
    }

    private func startConnect(allowTerms: Bool) {
        connecting = true
        connectProgress = 0
        lastConnectError = nil
        refreshCapability()
        resolveLocation { [weak self] result in
            guard let self else { return }
            switch result {
            case .failure(let e):
                self.connectFinished(allowTerms: allowTerms, error: e.reason == TapToPayReason.offline ? .offline : .other(e.reason),
                                     cloudReason: e.reason)
            case .success(let loc):
                self.reader.connect(locationId: loc, allowTermsAcceptance: allowTerms, progress: { p in
                    self.queue.async {
                        self.connectProgress = max(0, min(100, p))
                        self.refreshCapability()
                    }
                }, completion: { err in
                    self.queue.async { self.connectFinished(allowTerms: allowTerms, error: err, cloudReason: nil) }
                })
            }
        }
    }

    private func connectFinished(allowTerms: Bool, error: ReaderError?, cloudReason: String?) {
        connecting = false
        lastConnectError = cloudReason == nil ? error : nil
        lastCloudReason = cloudReason
        var waiters = connectWaiters
        connectWaiters = []
        if error == .termsNotAccepted && !allowTerms {
            let retry = waiters.filter { $0.allowTerms }
            waiters = waiters.filter { !$0.allowTerms }
            if !retry.isEmpty {
                connectWaiters = retry
                startConnect(allowTerms: true)
            }
        }
        refreshCapability()
        let err: ReaderError? = cloudReason.map { .unsupported($0) } ?? error
        for w in waiters { w.done(err) }
    }

    /// The Terminal Location to connect to, from onboarding_status (cached
    /// per process once known). Failure carries a TapToPayReason.
    private func resolveLocation(force: Bool = false, _ done: @escaping (Result<String, ReasonError>) -> Void) {
        if !force, let loc = locationId {
            done(.success(loc))
            return
        }
        cloudCall("onboarding_status", [:]) { [weak self] result in
            guard let self else { return }
            switch result {
            case .failure(let err):
                done(.failure(ReasonError(TapToPayReason.fromCloud(err.code), detail: err.code)))
            case .success(let s):
                let onboarded = (s["onboarded"] as? Bool) == true && (s["charges_enabled"] as? Bool) == true
                guard onboarded, let loc = s["location_id"] as? String, !loc.isEmpty else {
                    done(.failure(ReasonError(TapToPayReason.notOnboarded)))
                    return
                }
                self.locationId = loc
                done(.success(loc))
            }
        }
    }

    struct ReasonError: Error, Equatable {
        let reason: String
        /// The raw cloud code behind `reason`, for "Details for support".
        let detail: String?
        init(_ reason: String, detail: String? = nil) {
            self.reason = reason
            self.detail = detail
        }
    }

    /// Runs a blocking cloud call off the queue and hands the result back on it.
    private func cloudCall(_ op: String, _ body: [String: Any],
                           _ done: @escaping (Result<[String: Any], CloudError>) -> Void) {
        let cloud = self.cloud
        runBlocking { [weak self] in
            let result: Result<[String: Any], CloudError>
            do {
                result = .success(try cloud.call(op, body))
            } catch let e as CloudError {
                result = .failure(e)
            } catch {
                result = .failure(CloudError(code: "failed"))
            }
            self?.queue.async { done(result) }
        }
    }

    // MARK: prewarm / setup / education

    /// HIG: prepare at launch and after every return to the foreground —
    /// only once the manager's setup succeeded, and never with terms.
    func prewarm() {
        queue.async {
            guard self.current == nil, !self.setupInFlight, !self.connecting,
                  self.reader.support() == .supported, self.store.isSetupDone(self.provider),
                  !self.reader.isConnected else {
                self.refreshCapability()
                return
            }
            self.ensureConnected(allowTerms: false) { _ in }
        }
    }

    /// The manager's one-off preparation: location, onboarding, connect with
    /// Apple's terms allowed; How to Tap opens on the first success.
    func setup(completion: @escaping (SetupOutcome) -> Void) {
        queue.async {
            if case .unsupported(let reason) = self.reader.support() {
                completion(SetupOutcome(ok: false, reason: reason))
                return
            }
            if self.current != nil || self.setupInFlight {
                completion(SetupOutcome(ok: false, reason: TapToPayReason.busy))
                return
            }
            self.setupInFlight = true
            self.setupFailed = false
            let done: (String?, String?) -> Void = { reason, detail in
                self.setupInFlight = false
                self.setupFailed = reason != nil
                self.refreshCapability()
                completion(SetupOutcome(ok: reason == nil, reason: reason, detail: detail))
            }
            self.location.ensureWhenInUse { granted in
                self.queue.async {
                    guard granted else {
                        self.lastConnectError = .locationDenied
                        done(TapToPayReason.locationDenied, nil)
                        return
                    }
                    self.resolveLocation(force: true) { loc in
                        if case .failure(let e) = loc {
                            self.lastCloudReason = e.reason
                            done(e.reason, e.detail)
                            return
                        }
                        self.lastCloudReason = nil
                        self.ensureConnected(allowTerms: true) { err in
                            if let err {
                                let reason = err == .canceledByReader ? TapToPayReason.canceledByUser : err.reason
                                done(reason, err.detail)
                                return
                            }
                            let first = !self.store.isSetupDone(self.provider)
                            self.store.markSetupDone(self.provider)
                            if first {
                                self.education.presentHowToTap { _ in }
                            }
                            done(nil, nil)
                        }
                    }
                }
            }
        }
    }

    func showEducation(completion: @escaping (SetupOutcome) -> Void) {
        queue.async {
            if case .unsupported(let reason) = self.reader.support() {
                completion(SetupOutcome(ok: false, reason: reason))
                return
            }
            self.education.presentHowToTap { reason in
                completion(SetupOutcome(ok: reason == nil, reason: reason))
            }
        }
    }

    /// Test hook: drains work queued on the coordinator's queue (several
    /// rounds, since handlers enqueue follow-ups).
    func flush() {
        for _ in 0..<32 { queue.sync {} }
    }
}
