// The provider SDK as the coordinator sees it (ut-docs#3049). The Stripe
// adapter (StripeTerminalReader.swift) implements it over the Stripe
// Terminal iOS SDK; the unit tests implement it with a scripted fake.
import Foundation

enum SupportCheck: Equatable {
    case supported
    case unsupported(String) // a TapToPayReason
}

enum IntentStatus: Equatable {
    case requiresPaymentMethod, requiresConfirmation, requiresCapture, processing, succeeded, canceled, unknown
}

/// A PaymentIntent as far as the coordinator needs it. `opaque` carries the
/// SDK's own object back into the adapter.
struct ReaderIntent {
    var id: String
    var status: IntentStatus
    var amountMinor: Int64
    var currency: String
    var chargeId: String?
    var brand: String?
    var last4: String?
    var opaque: AnyObject?
}

enum ConfirmOutcome {
    case succeeded(ReaderIntent)
    /// `intent == nil`: the request timed out and the PaymentIntent's state
    /// is unknown — never a decline (Stripe: "Don't create a new one").
    case failed(intent: ReaderIntent?, declineCode: String?)
}

enum ReaderError: Error, Equatable {
    case canceledByUs        // our Cancelable.cancel took effect
    case canceledByReader    // the customer dismissed Apple's sheet
    case termsNotAccepted
    case entitlementMissing
    case locationDenied
    case unsupported(String) // a TapToPayReason
    case offline
    case other(String)

    var reason: String {
        switch self {
        case .termsNotAccepted: return TapToPayReason.termsNotAccepted
        case .entitlementMissing: return TapToPayReason.entitlementMissing
        case .locationDenied: return TapToPayReason.locationDenied
        case .unsupported(let r): return r
        case .offline: return TapToPayReason.offline
        case .canceledByUs, .canceledByReader, .other: return TapToPayReason.other
        }
    }

    /// The native error code of `.other` ("stripe 2900"), for support.
    var detail: String? {
        if case .other(let code) = self { return code }
        return nil
    }
}

protocol ReaderCancelable {
    /// Cancels; `done(true)` once the cancel took effect.
    func cancel(_ done: @escaping (Bool) -> Void)
}

protocol PaymentReader: AnyObject {
    /// Device checks only (idiom, OS, hardware); no network.
    func support() -> SupportCheck
    var isConnected: Bool { get }
    /// Discover the device's own reader and connect. `allowTermsAcceptance`
    /// is false on every cashier path: Apple's terms are shown only from the
    /// manager's Setup. `progress` is 0…100.
    func connect(locationId: String, allowTermsAcceptance: Bool,
                 progress: @escaping (Int) -> Void, completion: @escaping (ReaderError?) -> Void)
    func retrieveIntent(clientSecret: String, completion: @escaping (Result<ReaderIntent, ReaderError>) -> Void)
    func collect(_ intent: ReaderIntent, completion: @escaping (Result<ReaderIntent, ReaderError>) -> Void) -> ReaderCancelable
    /// Drops the connected reader; `completion` once it is gone (also when none was connected).
    func disconnect(completion: @escaping () -> Void)
    /// Cannot be cancelled once started.
    func confirm(_ intent: ReaderIntent, completion: @escaping (ConfirmOutcome) -> Void)
}

/// Asks for When-In-Use location (Stripe disables payments without it).
protocol LocationAuthorizer: AnyObject {
    func ensureWhenInUse(_ done: @escaping (Bool) -> Void)
}

/// Apple's "How to Tap" merchant education. `done(nil)` when shown, else a
/// reason.
protocol EducationPresenter: AnyObject {
    func presentHowToTap(_ done: @escaping (String?) -> Void)
}

/// Remembers that the manager's Setup succeeded once on this device, so
/// prewarm may connect without ever showing Apple's terms.
protocol SetupStore: AnyObject {
    func isSetupDone(_ provider: String) -> Bool
    func markSetupDone(_ provider: String)
}

final class UserDefaultsSetupStore: SetupStore {
    private let defaults: UserDefaults
    init(defaults: UserDefaults = .standard) { self.defaults = defaults }
    func isSetupDone(_ provider: String) -> Bool { defaults.bool(forKey: "ttp.setupDone.\(provider)") }
    func markSetupDone(_ provider: String) { defaults.set(true, forKey: "ttp.setupDone.\(provider)") }
}

/// Time, injectable so the tests drive deadlines by hand.
protocol TimerToken { func invalidate() }

protocol TapToPayClock {
    func nowMs() -> Int64
    /// Runs `block` on `queue` at `atMs` (or at once if that has passed).
    func schedule(atMs: Int64, on queue: DispatchQueue, _ block: @escaping () -> Void) -> TimerToken
}

final class SystemClock: TapToPayClock {
    private final class Token: TimerToken {
        let item: DispatchWorkItem
        init(_ item: DispatchWorkItem) { self.item = item }
        func invalidate() { item.cancel() }
    }

    func nowMs() -> Int64 { Int64(Date().timeIntervalSince1970 * 1000) }

    func schedule(atMs: Int64, on queue: DispatchQueue, _ block: @escaping () -> Void) -> TimerToken {
        let item = DispatchWorkItem(block: block)
        let delay = max(0, Double(atMs - nowMs()) / 1000)
        queue.asyncAfter(deadline: .now() + delay, execute: item)
        return Token(item)
    }
}
