// The reader link's frames (ut-docs#6538, ADR-0198 §6): the Swift mirror of
// internal/fleetlink/reader_frames.go and of Android's ReaderFrames.kt. The
// shared fixtures internal/fleetlink/testdata/reader_*.json pin all three.
// Strict on the way in (the main is the only sender, anything odd is dropped);
// on the way out only the fields the main's strict decoder accepts, so there
// is no field a card number, brand or last four could go in (ADR-0127).
//
// Core/ is pure Swift + Foundation: compiled into the host-less test bundle.
import Foundation

enum ReaderStatus: String, Equatable {
    case ready, busy, unavailable
    case needsSetup = "needs_setup"
}

enum ReaderResult: Equatable {
    case approved
    case declined(code: String?)
    case cancelled
    case pending(reason: String?)
}

/// One payment the main asks for. `clientSecret` lives in this value only: never
/// logged, persisted or shown (description below leaves it out).
struct ReaderCollect: CustomStringConvertible {
    var amountMinor: Int64
    var currency: String
    var key: String
    var timeoutMs: Int64
    var clientSecret: String
    var locationId: String

    var description: String {
        "ReaderCollect(amountMinor=\(amountMinor), currency=\(currency), key=\(key), timeoutMs=\(timeoutMs))"
    }
}

struct ReaderToken: CustomStringConvertible {
    var secret: String
    var locationId: String
    var description: String { "ReaderToken(location=\(locationId))" }
}

struct ReaderEnvelope {
    var id: String
    var type: String
    var replyTo: String?
    var payload: [String: Any]
}

enum ReaderFrames {
    static let maxFrameChars = 16 * 1024
    static let protocolVersion = 1
    static let maxAmountMinor: Int64 = 9_999_999
    static let maxTimeoutMs: Int64 = 600_000
    /// The closed set of status reasons (readerReasons in reader_frames.go).
    static let keptStatusReasons: Set<String> = ["reader_busy", "reader_unreachable", "reader_needs_setup"]
    private static let topLevelKeys: Set<String> = ["v", "id", "type", "reply_to", "payload"]

    // MARK: shape checks (the Go regexps, byte for byte)

    private static func isKey(_ s: String) -> Bool {   // ^[A-Za-z0-9_.:\-]{1,128}$
        guard (1...128).contains(s.utf8.count) else { return false }
        return s.utf8.allSatisfy { c in
            (c >= 0x30 && c <= 0x39) || (c >= 0x41 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a)
                || c == 0x5f || c == 0x2e || c == 0x3a || c == 0x2d
        }
    }

    static func isSlug(_ s: String) -> Bool {          // ^[a-z0-9_]{1,64}$
        guard (1...64).contains(s.utf8.count) else { return false }
        return s.utf8.allSatisfy { c in (c >= 0x30 && c <= 0x39) || (c >= 0x61 && c <= 0x7a) || c == 0x5f }
    }

    private static func isCurrency(_ s: String) -> Bool {   // ^[A-Za-z]{3}$
        s.utf8.count == 3 && s.utf8.allSatisfy { ($0 >= 0x41 && $0 <= 0x5a) || ($0 >= 0x61 && $0 <= 0x7a) }
    }

    /// A JSON integer; never a bool, a string or a fractional number.
    private static func integer(_ v: Any?) -> Int64? {
        guard let n = v as? NSNumber, CFGetTypeID(n) != CFBooleanGetTypeID() else { return nil }
        let d = n.doubleValue
        guard d == d.rounded(), abs(d) < 9_007_199_254_740_992 else { return nil }
        return n.int64Value
    }

    // MARK: envelope

    static func parse(_ text: String) -> ReaderEnvelope? {
        if text.count > maxFrameChars { return nil }
        guard let data = text.data(using: .utf8),
              let any = try? JSONSerialization.jsonObject(with: data),
              let j = any as? [String: Any] else { return nil }
        if !Set(j.keys).isSubset(of: topLevelKeys) { return nil }
        guard integer(j["v"]) == Int64(protocolVersion),
              let id = j["id"] as? String, let type = j["type"] as? String,
              !id.isEmpty, !type.isEmpty else { return nil }
        var payload: [String: Any] = [:]
        if let raw = j["payload"] {
            guard let p = raw as? [String: Any] else { return nil }
            payload = p
        }
        let reply = (j["reply_to"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        return ReaderEnvelope(id: id, type: type, replyTo: reply, payload: payload)
    }

    static func encode(id: String, type: String, payload: [String: Any], replyTo: String? = nil) -> String {
        var j: [String: Any] = ["v": protocolVersion, "id": id, "type": type, "payload": payload]
        if let replyTo { j["reply_to"] = replyTo }
        return serialise(j)
    }

    static func serialise(_ j: [String: Any]) -> String {
        guard let d = try? JSONSerialization.data(withJSONObject: j, options: [.sortedKeys, .withoutEscapingSlashes]) else { return "{}" }
        return String(decoding: d, as: UTF8.self)
    }

    // MARK: outgoing payloads

    static func helloPayload(appVersion: String) -> [String: Any] {
        ["role": "card_reader", "reader_protocol": protocolVersion, "app_version": appVersion, "platform": "ios"]
    }

    static func statusPayload(_ state: ReaderStatus, reason: String?) -> [String: Any] {
        var p: [String: Any] = ["state": state.rawValue]
        if let reason, keptStatusReasons.contains(reason) { p["reason"] = reason }
        return p
    }

    static func tokenRequestPayload() -> [String: Any] { [:] }
    static func ackPayload() -> [String: Any] { ["accepted": true] }
    static func errorPayload(code: String, retryable: Bool) -> [String: Any] { ["code": code, "retryable": retryable] }

    static func resultPayload(key: String, _ r: ReaderResult) -> [String: Any] {
        var p: [String: Any] = ["idempotency_key": key]
        switch r {
        case .approved:
            p["status"] = "approved"
        case .declined(let code):
            p["status"] = "declined"
            if let code, isSlug(code) { p["decline_code"] = code }
        case .cancelled:
            p["status"] = "cancelled"
        case .pending(let reason):
            p["status"] = "pending"
            if let reason, isSlug(reason) { p["reason"] = reason }
        }
        return p
    }

    // MARK: incoming payloads

    static func parseToken(_ p: [String: Any]) -> ReaderToken? {
        guard let s = p["secret"] as? String, let l = p["location_id"] as? String,
              !s.isEmpty, !l.isEmpty, s.count <= 512, l.count <= 128 else { return nil }
        return ReaderToken(secret: s, locationId: l)
    }

    static func parseCollect(_ p: [String: Any]) -> ReaderCollect? {
        guard (p["provider"] as? String) == "stripe",
              let amount = integer(p["amount_minor"]), amount >= 1, amount <= maxAmountMinor,
              let cur = p["currency"] as? String, isCurrency(cur),
              let key = p["idempotency_key"] as? String, isKey(key),
              let timeout = integer(p["timeout_ms"]), timeout >= 1, timeout <= maxTimeoutMs,
              let secret = p["client_secret"] as? String, let loc = p["location_id"] as? String,
              !secret.isEmpty, secret.count <= 512, !loc.isEmpty, loc.count <= 128 else { return nil }
        return ReaderCollect(amountMinor: amount, currency: cur.lowercased(), key: key, timeoutMs: timeout,
                             clientSecret: secret, locationId: loc)
    }

    static func parseCancelKey(_ p: [String: Any]) -> String? {
        (p["idempotency_key"] as? String).flatMap { isKey($0) ? $0 : nil }
    }
}
