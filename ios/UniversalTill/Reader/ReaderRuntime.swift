// Wires reader mode together (ut-docs#6538, ADR-0198 §6): Keychain store, the
// pinned link, the session, the shared Tap to Pay coordinator, discovery and the
// model. Exists only when the build's UT_READER_MODE (+ UT_TAP_TO_PAY) is on:
// with it off `ReaderController.shared` is nil, so no Keychain item is touched,
// no socket opened and no reader Bonjour browse run.
import Foundation
import UIKit

final class ReaderController: ReaderTokenRouter {
    static let enabled = ReaderFeature.isEnabled(info: Bundle.main.infoDictionary)
    static let shared: ReaderController? = ReaderController()

    let model = ReaderModel()
    private let queue = DispatchQueue(label: "com.universaltill.pos.reader")
    private let pairQueue = DispatchQueue(label: "com.universaltill.pos.reader.pair")
    private let store: ReaderStore
    private let session: ReaderSession
    private let coordinator: TapToPayCoordinator
    private let lock = NSLock()
    private var enrolledFlag = false
    private var link: ReaderLink?
    private var pairing: ReaderPairing?
    private var pairToken = 0
    private var foreground = false

    private init?() {
        guard let store = ReaderFeature.makeStore(info: Bundle.main.infoDictionary, factory: { KeychainReaderStore() }) else { return nil }
        self.store = store
        self.coordinator = TapToPayBridge.shared.coordinator
        let model = self.model
        let payments = CoordinatorReaderPayments(coordinator: coordinator, clock: SystemClock(), sessionQueue: queue)
        let ui = ReaderModelUi(model: model)
        let version = Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "0"
        self.session = ReaderSession(queue: queue, clock: SystemClock(), payments: payments, ui: ui, appVersion: version)
        DelegatingTokenProvider.shared.router = self
    }

    // MARK: lifecycle (scenePhase)

    /// Scene became .active.
    func activate() {
        queue.async {
            self.foreground = true
            let creds = self.store.load()
            self.setEnrolled(creds != nil)
            DispatchQueue.main.async { self.model.enrolled = creds != nil }
            self.session.onForeground()
            if let creds { self.startLink(creds) }
        }
    }

    /// Scene went to .background (never on .inactive: Apple's sheet causes that mid-collect).
    func deactivate() {
        queue.async {
            self.foreground = false
            self.session.onBackground()
            // Mid-collect the link stays: the SDK keeps running and the main holds the payment pending anyway.
            if !self.session.hasLive {
                self.link?.stop()
                self.link = nil
            }
            DispatchQueue.main.async { UIApplication.shared.isIdleTimerDisabled = false }
        }
    }

    private func startLink(_ creds: ReaderCredentials) {
        guard link == nil else { link?.start(); return }
        let l = ReaderLink(creds: creds, session: session,
                           reProof: PinReProof(http: URLSessionPairHttp(), nowMs: { SystemClock().nowMs() }),
                           onAdopted: { [weak self] c in self?.store.save(c) })
        link = l
        l.start()
    }

    private func setEnrolled(_ v: Bool) { lock.lock(); enrolledFlag = v; lock.unlock() }
    private var isEnrolled: Bool { lock.lock(); defer { lock.unlock() }; return enrolledFlag }

    // MARK: enrolment (C1/C2)

    func scan() {
        DispatchQueue.main.async { self.model.pair = .scanning }
        pairQueue.async {
            let found = MainDiscovery.scan()
            DispatchQueue.main.async {
                self.model.mains = found
                if self.model.pair == .scanning { self.model.pair = .idle }
            }
        }
    }

    func pair(address: String, tillId: String, mainName: String) {
        let device = UIDevice.current.name
        pairToken += 1
        let token = pairToken
        pairQueue.async {
            let p = ReaderPairing(http: URLSessionPairHttp(), nowMs: { SystemClock().nowMs() })
            switch p.start(address: address, tillId: tillId, deviceName: device) {
            case .failed(let reason):
                DispatchQueue.main.async { self.model.pair = .failed(reason) }
            case .waiting(let code):
                DispatchQueue.main.async { self.model.pair = .waiting(code: code); self.model.mainName = mainName }
                self.poll(p, token: token)
            }
        }
    }

    private func poll(_ p: ReaderPairing, token: Int) {
        pairQueue.async {
            guard token == self.pairToken else { return }
            var delay: TimeInterval = 2
            switch p.poll() {
            case .waiting: break
            case .later(let ms): delay = Double(ms) / 1000
            case .denied: DispatchQueue.main.async { self.model.pair = .failed("denied") }; return
            case .expired: DispatchQueue.main.async { self.model.pair = .failed("expired") }; return
            case .failed(let r): DispatchQueue.main.async { self.model.pair = .failed(r) }; return
            case .approved(let creds):
                self.store.save(creds)
                self.queue.async {
                    self.setEnrolled(true)
                    self.session.enrolled()
                    self.link?.stop(); self.link = nil
                    self.startLink(creds)
                }
                DispatchQueue.main.async { self.model.enrolled = true; self.model.pair = .idle }
                return
            }
            self.pairQueue.asyncAfter(deadline: .now() + delay) { self.poll(p, token: token) }
        }
    }

    func cancelPairing() {
        pairToken += 1
        DispatchQueue.main.async { self.model.pair = .idle }
    }

    func forget() {
        store.clear()
        queue.async {
            self.setEnrolled(false)
            self.link?.stop(); self.link = nil
            self.session.enrolled()
            self.session.onBackground()
            if self.foreground { self.session.onForeground() }
        }
        DispatchQueue.main.async { self.model.enrolled = false; self.model.screen = .connecting }
    }

    /// The phone's own Cancel on the request overlay, and the "Set up this phone" button.
    func cancelLive() { queue.async { self.session.cancelLive() } }
    func setup() { queue.async { self.session.setup() } }

    /// The main removed this phone: drop the credential (the session already showed "revoked").
    func credentialRevoked() {
        store.clear()
        queue.async { self.setEnrolled(false); self.link?.stop(); self.link = nil }
    }

    // MARK: ReaderTokenRouter (called from the Stripe SDK's thread)

    func route(_ done: @escaping (TokenSource) -> Void) {
        queue.async {
            let owner: CollectLockOwner
            switch self.coordinator.lockOwner() {
            case .till?: owner = .till
            case .reader?: owner = .reader
            case nil: owner = .none
            }
            done(ReaderTokenRouting.select(owner: owner, readerEnrolled: self.isEnrolled, linked: self.session.isLinked))
        }
    }

    func fetchFromLink(_ done: @escaping (TokenResult) -> Void) {
        queue.async { self.session.fetchToken(done) }
    }
}
