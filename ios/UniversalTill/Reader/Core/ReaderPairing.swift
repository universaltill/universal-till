// Enrolment (ADR-0198 §6.2, the protocol a joining till uses): a random secret
// stays on the phone, only its sha256 commitment goes out with role
// `card_reader`; TLS only, the certificate pin seen on the first contact is what
// makes the verification code, and every poll must meet that same pin. The
// manager approves on the main after comparing the code; the answer carries the
// reader's bearer and the pin it must keep dialling.
import CryptoKit
import Foundation
import Security

enum ReaderCrypto {
    static func hex<S: Sequence>(_ bytes: S) -> String where S.Element == UInt8 { bytes.map { String(format: "%02x", $0) }.joined() }
    static func sha256Hex(_ s: String) -> String { hex(SHA256.hash(data: Data(s.utf8))) }
    static func randomHex(_ n: Int) -> String {
        var b = [UInt8](repeating: 0, count: n)
        if SecRandomCopyBytes(kSecRandomDefault, n, &b) != errSecSuccess { fatalError("no secure randomness") }
        return hex(b)
    }
    static func hmacHex(key: String, message: String) -> String {
        hex(HMAC<SHA256>.authenticationCode(for: Data(message.utf8), using: SymmetricKey(data: Data(key.utf8))))
    }
}

enum PairStart: Equatable {
    case waiting(code: String)
    /// reason: needs_tls, refused, not_found
    case failed(reason: String)
}

enum PairPoll: Equatable {
    case waiting
    /// The main said "not now" (429 / 503): ask again later. Never a failed pairing.
    case later(afterMs: Int64)
    case expired
    case denied
    case approved(ReaderCredentials)
    case failed(reason: String)
}

/// HTTPS only. status -1 = no answer, -2 = the pin did not match; `pin` = the
/// leaf key pin the handshake saw.
struct PairAnswer {
    var status: Int
    var body: String
    var pin: String?
    var retryAfterSec: Int = 0
}

protocol PairHttp: AnyObject {
    func post(_ url: String, body: String, expectPin: String?) -> PairAnswer
    func get(_ url: String, headers: [String: String], expectPin: String?) -> PairAnswer
}

enum PairHttpLimits {
    /// The most of a response body ever read from the wire.
    static let maxBody = 8192
}

final class ReaderPairing {
    static let requestTtlMs: Int64 = 10 * 60_000
    static let secretHeader = "X-UT-Pair-Request-Secret"

    private let http: PairHttp
    private let rand: () -> String
    private let nowMs: () -> Int64
    private var base = ""
    private var address = ""
    private var secret = ""
    private var requestId = ""
    private var pin = ""
    private var startedAt: Int64 = 0

    init(http: PairHttp, rand: @escaping () -> String = { ReaderCrypto.randomHex(32) }, nowMs: @escaping () -> Int64) {
        self.http = http
        self.rand = rand
        self.nowMs = nowMs
    }

    /// derivedVerificationCode in internal/pages/pairing_api.go: six decimal
    /// digits of sha256(commitment + till id + pin).
    static func code(commitment: String, tillId: String, pin: String) -> String {
        let h = Array(SHA256.hash(data: Data((commitment + tillId + pin).utf8)))
        let v = (UInt64(h[0]) << 24) | (UInt64(h[1]) << 16) | (UInt64(h[2]) << 8) | UInt64(h[3])
        return String(format: "%06d", Int(v % 1_000_000))
    }

    /// host[:port] with any scheme and trailing slash removed.
    static func hostPort(_ addr: String) -> String {
        var a = addr.trimmingCharacters(in: .whitespaces)
        for p in ["https://", "http://"] where a.lowercased().hasPrefix(p) { a = String(a.dropFirst(p.count)) }
        while a.hasSuffix("/") { a.removeLast() }
        return a
    }

    private static func errorOf(_ body: String) -> String? {
        (try? JSONSerialization.jsonObject(with: Data(body.utf8)) as? [String: Any])?["error"] as? String
    }

    /// `tillId` is the main's id from its mDNS record ("id=") or typed by hand
    /// (G9): the code cannot be made without it. A plain-http address is refused
    /// before any request leaves the phone.
    func start(address addr: String, tillId: String, deviceName: String) -> PairStart {
        if addr.trimmingCharacters(in: .whitespaces).lowercased().hasPrefix("http://") { return .failed(reason: "needs_tls") }
        address = Self.hostPort(addr)
        if address.isEmpty || tillId.isEmpty { return .failed(reason: "not_found") }
        base = "https://\(address)"
        secret = rand()
        let commitment = ReaderCrypto.sha256Hex(secret)
        let body = ReaderFrames.serialise(["device_name": deviceName, "commitment": commitment, "role": "card_reader"])
        let a = http.post("\(base)/api/sync/pair-request", body: body, expectPin: nil)
        if a.status == -1 { return .failed(reason: "not_found") }
        if Self.errorOf(a.body) == "tls_required" { return .failed(reason: "needs_tls") }
        if a.status != 200 { return .failed(reason: "refused") }
        guard let seen = a.pin, !seen.isEmpty else { return .failed(reason: "needs_tls") }
        guard let obj = try? JSONSerialization.jsonObject(with: Data(a.body.utf8)) as? [String: Any],
              let d = obj["data"] as? [String: Any], let id = d["id"] as? String, !id.isEmpty else { return .failed(reason: "refused") }
        requestId = id
        pin = seen
        startedAt = nowMs()
        return .waiting(code: Self.code(commitment: commitment, tillId: tillId, pin: seen))
    }

    func poll() -> PairPoll {
        if nowMs() - startedAt > Self.requestTtlMs { return .expired }
        let a = http.get("\(base)/api/sync/pair-requests/\(requestId)", headers: [Self.secretHeader: secret], expectPin: pin)
        switch a.status {
        case 404, -1:
            return .waiting
        case 410:   // reaches only the holder of the request secret
            switch Self.errorOf(a.body) {
            case "pairing_denied": return .denied
            case "pairing_expired": return .expired
            default: return .failed(reason: "refused")
            }
        case 429, 503:
            return .later(afterMs: a.retryAfterSec > 0 ? min(60_000, Int64(a.retryAfterSec) * 1_000) : 2_000)
        case 200:
            guard let obj = try? JSONSerialization.jsonObject(with: Data(a.body.utf8)) as? [String: Any],
                  let d = obj["data"] as? [String: Any] else { return .failed(reason: "refused") }
            let rid = d["reader_id"] as? String ?? ""
            let bearer = d["bearer"] as? String ?? ""
            // the key the code was made over, and nothing else, is what this phone will ever trust
            if rid.isEmpty || bearer.isEmpty || (d["pin"] as? String) != pin { return .failed(reason: "refused") }
            return .approved(ReaderCredentials(address: address, readerId: rid, bearer: bearer, pin: pin))
        default:
            return .failed(reason: "refused")   // -2 (another certificate), a limit, no reader method
        }
    }
}
