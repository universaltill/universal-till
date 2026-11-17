// The ut-cloud payment broker as the coordinator sees it (ut-docs#3049,
// #6454). GoCloudClient implements it over mobile.TapToPayCloud →
// internal/paybroker.Call, which holds the device credential in Go; the
// tests implement it with a fake. Calls block (up to paybroker's 15 s), so
// the coordinator never makes one on its own queue.
import Foundation

struct CloudError: Error, Equatable {
    /// mobile.TapToPayCloud's stable error code: the cloud's own code
    /// (not_onboarded, invalid_request, provider_unavailable), http_<n>,
    /// op_not_allowed, not_enrolled, offline, response_too_large,
    /// request_too_large, insecure_endpoint, failed —
    /// or bad_response when the body was not a JSON object.
    let code: String
}

protocol TapToPayCloudClient {
    func call(_ op: String, _ body: [String: Any]) throws -> [String: Any]
}
