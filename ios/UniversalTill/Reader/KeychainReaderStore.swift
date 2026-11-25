// The reader credential in the Keychain: this device only, only while unlocked,
// never synced, never in a backup (this-device-only items are not restored to
// another device). One generic-password item holding the four fields as JSON.
// Never UserDefaults, never logged.
import Foundation
import Security

final class KeychainReaderStore: ReaderStore {
    private let service = "com.universaltill.pos.reader"
    private let account = "credentials"

    private var base: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account,
         kSecAttrSynchronizable as String: false]
    }

    func load() -> ReaderCredentials? {
        var q = base
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data,
              let j = try? JSONSerialization.jsonObject(with: data) as? [String: String],
              let a = j["address"], let r = j["reader_id"], let b = j["bearer"], let p = j["pin"] else { return nil }
        return ReaderCredentials(address: a, readerId: r, bearer: b, pin: p)
    }

    func save(_ c: ReaderCredentials) {
        guard let data = try? JSONSerialization.data(withJSONObject: ["address": c.address, "reader_id": c.readerId,
                                                                       "bearer": c.bearer, "pin": c.pin]) else { return }
        clear()
        var add = base
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        SecItemAdd(add as CFDictionary, nil)
    }

    func clear() {
        SecItemDelete(base as CFDictionary)
    }
}
