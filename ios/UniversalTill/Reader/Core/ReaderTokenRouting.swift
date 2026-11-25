// Which party answers the Stripe SDK's connection-token request (design D3):
// the SDK owns ONE ConnectionTokenProvider per process, so a single delegating
// provider evaluates this rule on every fetch. Never cached.
import Foundation

enum TokenSource: Equatable { case cloud, link }
enum CollectLockOwner: Equatable { case none, till, reader }

enum ReaderTokenRouting {
    static func select(owner: CollectLockOwner, readerEnrolled: Bool, linked: Bool) -> TokenSource {
        switch owner {
        case .till: return .cloud
        case .reader: return .link
        case .none: return readerEnrolled && linked ? .link : .cloud
        }
    }
}

/// The link side of the token seam: ReaderSession.requestToken answers it.
protocol ReaderTokenSource: AnyObject {
    func fetchToken(_ done: @escaping (TokenResult) -> Void)
}
