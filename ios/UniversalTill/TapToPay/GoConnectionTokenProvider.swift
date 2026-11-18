// Stripe Terminal's ConnectionTokenProvider over the cloud broker
// (ut-docs#3049): connection_token through mobile.TapToPayCloud. Fetched off
// the main thread, and never cached ("Don't cache or hardcode the
// connection token", https://docs.stripe.com/terminal/payments/setup-integration?terminal-sdk-platform=ios).
import Foundation
import StripeTerminal

final class GoConnectionTokenProvider: NSObject, ConnectionTokenProvider {
    private let cloud = GoCloudClient(provider: "stripe")

    func fetchConnectionToken(_ completion: @escaping ConnectionTokenCompletionBlock) {
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                let reply = try self.cloud.call("connection_token", [:])
                guard let secret = reply["secret"] as? String, !secret.isEmpty else {
                    completion(nil, NSError(domain: "UniversalTill.TapToPay", code: 1,
                                            userInfo: [NSLocalizedDescriptionKey: "no connection token"]))
                    return
                }
                completion(secret, nil)
            } catch let e as CloudError {
                completion(nil, NSError(domain: "UniversalTill.TapToPay", code: 2,
                                        userInfo: [NSLocalizedDescriptionKey: e.code]))
            } catch {
                completion(nil, error)
            }
        }
    }
}
