// The cloud payment broker over the Go till (ut-docs#3049):
// mobile.TapToPayCloud → internal/paybroker.Call, in-process. Go holds the
// ADR-0116 device credential and the op allow-list; this side never sees
// either, and never logs a body (it carries the connection-token secret and
// the PaymentIntent client secret).
import Foundation
import Mobile

struct GoCloudClient: TapToPayCloudClient {
    let provider: String

    /// Blocks for up to paybroker's 15 s: never call on the main thread.
    func call(_ op: String, _ body: [String: Any]) throws -> [String: Any] {
        dispatchPrecondition(condition: .notOnQueue(.main))
        let data = try JSONSerialization.data(withJSONObject: body)
        var err: NSError?
        let out = MobileTapToPayCloud(provider, op, String(decoding: data, as: UTF8.self), &err)
        if let err {
            // mobile.TapToPayCloud's error text is a stable code.
            throw CloudError(code: err.localizedDescription)
        }
        guard let obj = try? JSONSerialization.jsonObject(with: Data(out.utf8)) as? [String: Any] else {
            throw CloudError(code: "bad_response")
        }
        return obj
    }
}
