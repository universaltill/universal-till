// The build's reader-mode switch (project.yml UT_READER_MODE -> Info.plist
// UTReaderModeEnabled). Debug only, default NO, and it needs the Tap to Pay
// switch too: the collect runs on the same Stripe Terminal reader and the same
// development-only entitlement (ADR-0198 §6, ut-docs#3047). Off means: no
// reader entry, no Keychain access, no socket, no Bonjour browse for readers.
import Foundation

enum ReaderFeature {
    static func isEnabled(info: [String: Any]?) -> Bool {
        (info?["UTReaderModeEnabled"] as? String) == "YES" && TapToPayFeature.isEnabled(info: info)
    }

    /// Builds the credential store only when the feature is on; with it off the
    /// factory is never called, so no Keychain item is read or written.
    static func makeStore(info: [String: Any]?, factory: () -> ReaderStore) -> ReaderStore? {
        isEnabled(info: info) ? factory() : nil
    }
}
