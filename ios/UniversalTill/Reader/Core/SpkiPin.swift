// The main till's certificate identity is its public key: the hex SHA-256 of
// the SubjectPublicKeyInfo, the value internal/lantls.PinOf makes (ADR-0114
// §7). The main's key is ECDSA P-256 (lantls/cert.go); Security hands back the
// raw X9.63 point, so the SPKI is the fixed P-256 header plus that point. Any
// other key type or size is refused: fail closed. There is no CA validation:
// the phone trusts exactly the key it saw at pairing.
import CryptoKit
import Foundation
import Security

enum SpkiPin {
    /// ASN.1 prefix of a P-256 SubjectPublicKeyInfo (id-ecPublicKey, prime256v1, BIT STRING 66 bytes).
    private static let p256Header: [UInt8] = [
        0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01,
        0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00,
    ]

    static func hex(_ bytes: [UInt8]) -> String { bytes.map { String(format: "%02x", $0) }.joined() }

    /// Pin of an uncompressed P-256 point (0x04 || X || Y, 65 bytes); nil for anything else.
    static func pin(ofP256Point point: Data) -> String? {
        guard point.count == 65, point.first == 0x04 else { return nil }
        return hex(Array(SHA256.hash(data: Data(p256Header) + point)))
    }

    static func pin(of key: SecKey) -> String? {
        guard let attrs = SecKeyCopyAttributes(key) as? [CFString: Any],
              let type = attrs[kSecAttrKeyType] as? String,
              type == (kSecAttrKeyTypeECSECPrimeRandom as String),
              (attrs[kSecAttrKeySizeInBits] as? Int) == 256,
              let ext = SecKeyCopyExternalRepresentation(key, nil) as Data? else { return nil }
        return pin(ofP256Point: ext)
    }

    static func pin(ofCertificate cert: SecCertificate) -> String? {
        SecCertificateCopyKey(cert).flatMap { pin(of: $0) }
    }

    /// Constant time: the pin is public, but there is no reason to be clever.
    static func matches(_ a: String, _ b: String) -> Bool {
        let x = Array(a.utf8), y = Array(b.utf8)
        guard x.count == y.count, !x.isEmpty else { return false }
        var diff: UInt8 = 0
        for i in 0..<x.count { diff |= x[i] ^ y[i] }
        return diff == 0
    }

    /// The decision the URLSession server-trust challenge makes, before any
    /// request bytes exist (so the Bearer header is never sent to an unpinned peer).
    enum Decision: Equatable { case trust, reject }

    static func decide(leaf: SecCertificate?, pinned: String) -> Decision {
        guard let leaf, !pinned.isEmpty, let seen = pin(ofCertificate: leaf), matches(seen, pinned) else { return .reject }
        return .trust
    }
}
