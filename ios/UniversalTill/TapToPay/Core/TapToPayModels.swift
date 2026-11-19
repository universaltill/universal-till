// Tap to Pay on iPhone (ut-docs#3049, ADR-0125 §3): the JSON shapes that
// cross the gomobile boundary, the capability report, and the build switch.
//
// Core/ is pure Swift + Foundation — no Mobile, StripeTerminal, UIKit,
// ProximityReader or CoreLocation — so UniversalTillTapToPayTests compiles
// it straight into a host-less test bundle.
import Foundation

/// ADR-0125 §3f outcome vocabulary. Anything the till cannot be sure of is
/// `pending`, never `declined` (#6439).
enum TapToPayStatus: String, Codable, Equatable {
    case approved, declined, cancelled, pending
}

/// One payment, as internal/devicepay.Request sends it (snake_case JSON;
/// golden file internal/devicepay/testdata/request_golden.json).
struct CollectRequest: Codable, Equatable {
    var provider: String
    var amountMinor: Int64
    var currency: String
    var idempotencyKey: String
    var description: String?
    var deadlineUnixMs: Int64

    enum CodingKeys: String, CodingKey {
        case provider
        case amountMinor = "amount_minor"
        case currency
        case idempotencyKey = "idempotency_key"
        case description
        case deadlineUnixMs = "deadline_unix_ms"
    }
}

/// The verdict, as internal/devicepay.Result reads it — strictly: Go
/// rejects any field not listed here, so no card data can ride along.
/// Optional fields are omitted when nil (synthesized encodeIfPresent).
struct CollectResult: Codable, Equatable {
    var status: TapToPayStatus
    var providerTxnId: String?
    var chargeId: String?
    var cardBrand: String?
    var last4: String?
    var declineCode: String?
    var reason: String?

    enum CodingKeys: String, CodingKey {
        case status
        case providerTxnId = "provider_txn_id"
        case chargeId = "charge_id"
        case cardBrand = "card_brand"
        case last4
        case declineCode = "decline_code"
        case reason
    }

    init(status: TapToPayStatus, providerTxnId: String? = nil, chargeId: String? = nil,
         cardBrand: String? = nil, last4: String? = nil, declineCode: String? = nil,
         reason: String? = nil) {
        self.status = status
        self.providerTxnId = providerTxnId
        self.chargeId = chargeId
        self.cardBrand = cardBrand
        self.last4 = last4
        self.declineCode = declineCode
        self.reason = reason
    }

    static func declined(_ reason: String, code: String? = nil) -> CollectResult {
        CollectResult(status: .declined, declineCode: code, reason: reason)
    }
    static func cancelled(_ reason: String) -> CollectResult { CollectResult(status: .cancelled, reason: reason) }
    static func pending(_ reason: String) -> CollectResult { CollectResult(status: .pending, reason: reason) }
}

/// Setup's and ShowEducation's answer (devicepay.SetupResult).
struct SetupOutcome: Codable, Equatable {
    var ok: Bool
    var reason: String?
}

enum CapabilityState: String, Codable, Equatable {
    case ready, preparing
    case needsSetup = "needs_setup"
    case unavailable
}

/// One provider's readiness (devicepay.ProviderCap).
struct Capability: Codable, Equatable {
    var ready: Bool
    var state: CapabilityState
    var reason: String?
    var progress: Int

    static func of(_ state: CapabilityState, reason: String? = nil, progress: Int = 0) -> Capability {
        Capability(ready: state == .ready, state: state, reason: reason,
                   progress: state == .ready ? 100 : progress)
    }
}

/// Reason strings shared with Go and the settings UI (#6458's
/// settings.payments.ttp.reason.* keys).
enum TapToPayReason {
    static let ipad = "ipad"
    static let iosTooOld = "ios_too_old"
    static let unsupportedDevice = "unsupported_device"
    static let entitlementMissing = "entitlement_missing"
    static let notOnboarded = "not_onboarded"
    static let notEnrolled = "not_enrolled"
    static let termsNotAccepted = "terms_not_accepted"
    static let locationDenied = "location_denied"
    static let offline = "offline"
    static let other = "other"

    static let busy = "busy"
    static let amountMismatch = "amount_mismatch"
    static let intentCanceled = "intent_canceled"
    static let readFailed = "read_failed"
    static let timeout = "timeout"
    static let `operator` = "operator"
    static let customer = "customer"
    static let outcomeUnknown = "outcome_unknown"
    static let badRequest = "bad_request"
    static let unsupportedProvider = "unsupported_provider"

    /// Cloud error codes (mobile.TapToPayCloud's error text) the UI knows;
    /// anything else reads as `other`.
    static func fromCloud(_ code: String) -> String {
        switch code {
        case notOnboarded, offline, notEnrolled, "invalid_request", "provider_unavailable":
            return code
        default:
            return other
        }
    }
}

/// The build's Tap to Pay switch (project.yml UT_TAP_TO_PAY → Info.plist
/// UTTapToPayEnabled). Only the exact string "YES" turns it on; when it is
/// off the bridge is never registered and nothing is shown anywhere.
enum TapToPayFeature {
    static func isEnabled(info: [String: Any]?) -> Bool {
        (info?["UTTapToPayEnabled"] as? String) == "YES"
    }

    /// Stripe's simulated Tap to Pay reader (UT_TAP_TO_PAY_SIMULATED).
    static func isSimulated(info: [String: Any]?) -> Bool {
        (info?["UTTapToPaySimulated"] as? String) == "YES"
    }
}
