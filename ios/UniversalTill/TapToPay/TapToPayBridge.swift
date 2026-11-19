// The Swift side of mobile.TapToPayBridge (ut-docs#3049, ADR-0125 §3):
// plugin → host fn (#6455) → internal/devicepay → this bridge → the
// coordinator → Stripe Terminal. The web page never starts a payment or
// sees a result through here — no WKScriptMessageHandler is involved, so
// the approval verdict never lives in JS.
//
// Registered only when the build's UT_TAP_TO_PAY flag is on
// (TapToPayRegistration): with the flag off nothing here is ever created,
// Go's devicepay.Capabilities is empty, and nothing is shown anywhere.
import Foundation
import Mobile

final class TapToPayBridge: NSObject, MobileTapToPayBridgeProtocol {
    static let shared = TapToPayBridge()

    /// Backstop past the request's own deadline (the coordinator answers by
    /// deadline − 3 s; Go cancels at the deadline).
    static let collectBackstopMs: Int64 = 5_000
    /// Setup waits on a person (location prompt, Apple's terms).
    static let setupBackstop: DispatchTimeInterval = .seconds(600)

    let coordinator: TapToPayCoordinator
    let status = TapToPayStatusModel()

    override init() {
        let info = Bundle.main.infoDictionary
        coordinator = TapToPayCoordinator(
            reader: StripeTerminalReader(simulated: TapToPayFeature.isSimulated(info: info)),
            cloud: GoCloudClient(provider: "stripe"),
            clock: SystemClock(),
            location: CoreLocationAuthorizer(),
            education: ProximityReaderEducation(),
            store: UserDefaultsSetupStore())
        super.init()
        let status = self.status
        coordinator.observeStatus { cap, setupFailed in
            DispatchQueue.main.async { status.update(cap, setupFailed: setupFailed) }
        }
    }

    // MARK: MobileTapToPayBridgeProtocol (called on Go threads)

    func capabilities() -> String {
        coordinator.capabilitiesJSON()
    }

    func collect(_ requestJSON: String?) -> String {
        dispatchPrecondition(condition: .notOnQueue(.main))
        guard let data = requestJSON?.data(using: .utf8),
              let req = try? JSONDecoder().decode(CollectRequest.self, from: data) else {
            return Self.encode(CollectResult.declined(TapToPayReason.badRequest))
        }
        let box = ResultBox<CollectResult>()
        coordinator.collect(req) { box.set($0) }
        let waitMs = max(0, req.deadlineUnixMs + Self.collectBackstopMs - SystemClock().nowMs())
        guard let result = box.wait(.milliseconds(Int(min(waitMs, Int64(Int32.max))))) else {
            coordinator.cancel(idempotencyKey: req.idempotencyKey)
            return Self.encode(CollectResult.pending(TapToPayReason.timeout))
        }
        return Self.encode(result)
    }

    func cancel(_ idempotencyKey: String?) {
        coordinator.cancel(idempotencyKey: idempotencyKey ?? "")
    }

    func setup(_ provider: String?) -> String {
        dispatchPrecondition(condition: .notOnQueue(.main))
        guard provider == coordinator.provider else {
            return Self.encode(SetupOutcome(ok: false, reason: TapToPayReason.unsupportedProvider))
        }
        let box = ResultBox<SetupOutcome>()
        coordinator.setup { box.set($0) }
        return Self.encode(box.wait(Self.setupBackstop) ?? SetupOutcome(ok: false, reason: TapToPayReason.timeout))
    }

    func showEducation(_ provider: String?) -> String {
        dispatchPrecondition(condition: .notOnQueue(.main))
        guard provider == coordinator.provider else {
            return Self.encode(SetupOutcome(ok: false, reason: TapToPayReason.unsupportedProvider))
        }
        let box = ResultBox<SetupOutcome>()
        coordinator.showEducation { box.set($0) }
        return Self.encode(box.wait(Self.setupBackstop) ?? SetupOutcome(ok: false, reason: TapToPayReason.timeout))
    }

    private static func encode<T: Encodable>(_ value: T) -> String {
        guard let data = try? JSONEncoder().encode(value) else { return "{}" }
        return String(decoding: data, as: UTF8.self)
    }
}

/// One value handed from a callback to a blocked Go thread.
private final class ResultBox<T> {
    private let sem = DispatchSemaphore(value: 0)
    private let lock = NSLock()
    private var value: T?

    func set(_ v: T) {
        lock.lock()
        let first = value == nil
        if first { value = v }
        lock.unlock()
        if first { sem.signal() }
    }

    func wait(_ timeout: DispatchTimeInterval) -> T? {
        guard sem.wait(timeout: .now() + timeout) == .success else { return nil }
        lock.lock(); defer { lock.unlock() }
        return value
    }
}

/// Registers the bridge per ADR-0125 §3e — only when the build flag is on.
enum TapToPayRegistration {
    static let enabled = TapToPayFeature.isEnabled(info: Bundle.main.infoDictionary)

    /// Scene became .active: register, then prepare the reader (HIG: "as
    /// soon as your app starts and immediately after each transition to
    /// the foreground").
    static func activate() {
        guard enabled else { return }
        MobileSetTapToPayBridge(TapToPayBridge.shared)
        TapToPayBridge.shared.coordinator.prewarm()
    }

    /// Scene went to .background. Never called on .inactive: Apple's own
    /// terms and tap sheets make the scene inactive mid-payment.
    static func deactivate() {
        guard enabled else { return }
        MobileSetTapToPayBridge(nil)
    }
}
