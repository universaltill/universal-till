// The Tap to Pay coordinator as reader mode uses it (ut-docs#6538, design 2.2).
// A `reader.collect` is the same payment a till's own tender is, minus the
// PaymentIntent creation (the main made it): the coordinator's collectIntent
// takes the ONE collect lock, so reader mode has no second lock of its own.
import Foundation

/// How a collect ended, in the reader link's vocabulary. Anything unsure is
/// pending, never declined; the main re-derives every fact from the broker.
enum ReaderOutcome: Equatable {
    case result(ReaderResult)
    /// Nothing was collected and no card was read (setup/permission/intent
    /// problems): reported as cancelled, and the status turns needs_setup.
    case notStarted
}

enum ReaderOutcomeMapper {
    /// Pending reasons the phone may send (the main only validates the slug shape).
    static let pendingReasons: Set<String> = ["amount_mismatch", "outcome_unknown", "timeout", "phone_pending",
                                              "unknown_pending", "key_mismatch"]
    /// Declined-with-reason from the coordinator that mean "no card was read".
    static let preCollectReasons: Set<String> = [
        TapToPayReason.busy, TapToPayReason.locationDenied, TapToPayReason.termsNotAccepted,
        TapToPayReason.entitlementMissing, TapToPayReason.ipad, TapToPayReason.iosTooOld,
        TapToPayReason.unsupportedDevice, TapToPayReason.notOnboarded, TapToPayReason.notEnrolled,
        TapToPayReason.offline, TapToPayReason.other, TapToPayReason.readFailed, TapToPayReason.intentCanceled,
        TapToPayReason.badRequest, TapToPayReason.unsupportedProvider,
    ]

    static func map(_ r: CollectResult) -> ReaderOutcome {
        switch r.status {
        case .approved:
            return .result(.approved)
        case .cancelled:
            return .result(.cancelled)
        case .pending:
            let reason = r.reason.flatMap { pendingReasons.contains($0) ? $0 : nil } ?? "phone_pending"
            return .result(.pending(reason: reason))
        case .declined:
            guard let reason = r.reason else {
                // a card the SDK/bank refused
                let code = r.declineCode.flatMap { ReaderFrames.isSlug($0) ? $0 : nil }
                return .result(.declined(code: code))
            }
            if reason == TapToPayReason.amountMismatch {
                // the main re-derives from the broker; a wrong decline double-charges
                return .result(.pending(reason: "amount_mismatch"))
            }
            if preCollectReasons.contains(reason) || reason.hasPrefix("unsupported_") || reason.hasPrefix("device_") {
                return .notStarted
            }
            return .result(.pending(reason: "phone_pending"))
        }
    }
}

protocol ReaderPayments: AnyObject {
    func capability() -> Capability
    /// The one-off preparation (permission, terms, connect); nil = ready, else a reason.
    func setup(_ cb: @escaping (String?) -> Void)
    /// `started(false)` = refused as busy, nothing begun; `end` then never fires.
    /// Both callbacks arrive on the session's queue.
    func collect(_ c: ReaderCollect, remainingMs: Int64, started: @escaping (Bool) -> Void,
                 end: @escaping (ReaderOutcome) -> Void)
    func cancel(key: String)
    /// Drops the SDK's reader connection while idle (ADR-0198 §6.4).
    func disconnect()
    /// The lock was taken or freed by the till's own tender or setup (reader
    /// collects are reported by the session itself). Fired on the session's queue.
    var onTillBusyChange: ((Bool) -> Void)? { get set }
}

/// ReaderPayments over TapToPayCoordinator, with the phone's ONE local attempt
/// record (review A-2 of the Android port): a key whose outcome is unknown stays
/// refused as pending; a NEW key means the main resolved the old one, so the old
/// record is dropped here (local state only), or after `unknownTtlMs` since it
/// was first refused. Bounded.
final class CoordinatorReaderPayments: ReaderPayments {
    static let unknownTtlMs: Int64 = 30 * 60_000
    static let maxRecords = 8

    private let coordinator: TapToPayCoordinator
    private let clock: TapToPayClock
    private let sessionQueue: DispatchQueue
    private var unresolved: [String: Int64] = [:]   // key -> first time left unresolved
    var onTillBusyChange: ((Bool) -> Void)?

    init(coordinator: TapToPayCoordinator, clock: TapToPayClock, sessionQueue: DispatchQueue) {
        self.coordinator = coordinator
        self.clock = clock
        self.sessionQueue = sessionQueue
        coordinator.observeBusy { [weak self] owner in
            guard let self else { return }
            // only the till's own use is news to the session; reader collects are its own
            if owner != .reader { self.sessionQueue.async { self.onTillBusyChange?(owner == .till) } }
        }
    }

    func capability() -> Capability { coordinator.capability() }

    func setup(_ cb: @escaping (String?) -> Void) {
        coordinator.setup { out in self.sessionQueue.async { cb(out.ok ? nil : (out.reason ?? TapToPayReason.other)) } }
    }

    func collect(_ c: ReaderCollect, remainingMs: Int64, started: @escaping (Bool) -> Void,
                 end: @escaping (ReaderOutcome) -> Void) {
        let now = clock.nowMs()
        // The same key whose outcome is unknown: never a second tap.
        if let since = unresolved[c.key], now - since < Self.unknownTtlMs {
            sessionQueue.async {
                started(true)
                end(.result(.pending(reason: "outcome_unknown")))
            }
            return
        }
        unresolved.removeAll()   // a new key (or an expired one): the main resolved the old attempt
        let req = CollectRequest(provider: coordinator.provider, amountMinor: c.amountMinor, currency: c.currency,
                                 idempotencyKey: c.key, description: nil, deadlineUnixMs: now + remainingMs)
        var refusedBusy = false
        var startedCalled = false
        coordinator.collectIntent(req, clientSecret: c.clientSecret, locationId: c.locationId, started: { ok in
            startedCalled = true
            if !ok { refusedBusy = true }
            self.sessionQueue.async { started(ok) }
        }, completion: { result in
            if refusedBusy { return }
            if !startedCalled {
                // Refused before the lock (provider, bad request, unsupported): nothing began and no ack went
                // out, so answer "not now" instead of a result the main would time out on.
                refusedBusy = true
                self.sessionQueue.async { started(false) }
                return
            }
            let outcome = ReaderOutcomeMapper.map(result)
            self.sessionQueue.async {
                if case .result(.pending) = outcome {
                    if self.unresolved.count >= Self.maxRecords, let oldest = self.unresolved.min(by: { $0.value < $1.value }) {
                        self.unresolved.removeValue(forKey: oldest.key)
                    }
                    self.unresolved[c.key] = self.clock.nowMs()
                }
                end(outcome)
            }
        })
    }

    func cancel(key: String) { coordinator.cancel(idempotencyKey: key) }
    func disconnect() { coordinator.disconnectIfIdle() }
}
