// The one Stripe Terminal ConnectionTokenProvider per process (the SDK allows a
// single init). Each fetch asks the router who answers (design D3): the till's
// own credential through the cloud broker, or, in reader mode, the main over the
// reader link (`reader.token`). Without a router (reader mode off) it is exactly
// the old cloud provider. Never cached, never logged.
import Foundation
import StripeTerminal

protocol ReaderTokenRouter: AnyObject {
    /// Decides, then reports on any queue.
    func route(_ done: @escaping (TokenSource) -> Void)
    func fetchFromLink(_ done: @escaping (TokenResult) -> Void)
}

final class DelegatingTokenProvider: NSObject, ConnectionTokenProvider {
    static let shared = DelegatingTokenProvider()

    private let cloud = GoConnectionTokenProvider()
    private let lock = NSLock()
    private weak var _router: ReaderTokenRouter?
    var router: ReaderTokenRouter? {
        get { lock.lock(); defer { lock.unlock() }; return _router }
        set { lock.lock(); _router = newValue; lock.unlock() }
    }

    func fetchConnectionToken(_ completion: @escaping ConnectionTokenCompletionBlock) {
        guard let router else {
            cloud.fetchConnectionToken(completion)
            return
        }
        router.route { source in
            switch source {
            case .cloud:
                self.cloud.fetchConnectionToken(completion)
            case .link:
                router.fetchFromLink { result in
                    switch result {
                    case .ok(let secret, _):
                        completion(secret, nil)
                    case .err(let code):
                        completion(nil, NSError(domain: "UniversalTill.Reader", code: 3,
                                                userInfo: [NSLocalizedDescriptionKey: code]))
                    }
                }
            }
        }
    }
}
