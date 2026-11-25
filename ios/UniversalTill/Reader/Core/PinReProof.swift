// Pin-mismatch recovery (ADR-0198 §6.2 "as for tills"). When the main's LAN key
// changed, the pinned handshake fails and the bearer was never sent. This asks
// the main to prove it knows the bearer: POST /api/sync/primary-proof over a
// connection that records the leaf pin it saw (never carrying the bearer), then
// checks HMAC-SHA256(key = ascii(sha256hex(bearer)),
// "ut-primary-proof-v1\n" primary_id "\n" reader_id "\n" nonce "\n" host:port "\n" pin_seen "\n").
// Only a match adopts pin_seen; at most one attempt per 30 s.
import CryptoKit
import Foundation

enum ReProof: Equatable {
    case adopted(ReaderCredentials)
    case mismatch
    /// No proof from the unverified connection: the word of an unauthenticated
    /// peer never clears the pairing; keep the pairing and the old pin.
    case unreachable
    case tooSoon
}

final class PinReProof {
    static let minGapMs: Int64 = 30_000
    private static let label = "ut-primary-proof-v1"
    private let http: PairHttp
    private let rand: () -> String
    private let nowMs: () -> Int64
    private var lastAt: Int64?

    init(http: PairHttp, rand: @escaping () -> String = { ReaderCrypto.randomHex(32) }, nowMs: @escaping () -> Int64) {
        self.http = http
        self.rand = rand
        self.nowMs = nowMs
    }

    func attempt(_ c: ReaderCredentials, address: String? = nil) -> ReProof {
        let now = nowMs()
        if let last = lastAt, now - last < Self.minGapMs { return .tooSoon }
        lastAt = now
        let hostPort = ReaderPairing.hostPort(address ?? c.address)
        let nonce = rand()
        let body = ReaderFrames.serialise(["till_id": c.readerId, "nonce": nonce])
        let a = http.post("https://\(hostPort)/api/sync/primary-proof", body: body, expectPin: nil)
        if a.status != 200 { return .unreachable }
        guard let seen = a.pin, !seen.isEmpty else { return .mismatch }
        guard let obj = try? JSONSerialization.jsonObject(with: Data(a.body.utf8)) as? [String: Any],
              let d = obj["data"] as? [String: Any],
              let primary = d["primary_till_id"] as? String, !primary.isEmpty else { return .mismatch }
        let want = ReaderCrypto.hmacHex(key: ReaderCrypto.sha256Hex(c.bearer),
                                        message: "\(Self.label)\n\(primary)\n\(c.readerId)\n\(nonce)\n\(hostPort)\n\(seen)\n")
        // both, so a rotated bearer's grace proof counts
        var ok = false
        for got in [d["proof"] as? String ?? "", d["prev_proof"] as? String ?? ""] {
            if SpkiPin.matches(want, got) { ok = true }
        }
        guard ok else { return .mismatch }
        var next = c
        next.address = hostPort
        next.pin = seen
        return .adopted(next)
    }
}
