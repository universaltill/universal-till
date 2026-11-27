// What the phone keeps after the main approved it (ADR-0198 §6.2). The bearer
// and the pin live in the Keychain (KeychainReaderStore), never UserDefaults,
// never logged. No card data and no shop data is ever stored here.
import Foundation

struct ReaderCredentials: Equatable, CustomStringConvertible {
    var address: String
    var readerId: String
    var bearer: String
    var pin: String
    var description: String { "ReaderCredentials(address=\(address), readerId=\(readerId))" }
}

protocol ReaderStore: AnyObject {
    func load() -> ReaderCredentials?
    func save(_ c: ReaderCredentials)
    func clear()
}

/// Hold-in-memory store for tests and previews.
final class MemoryReaderStore: ReaderStore {
    private(set) var loads = 0
    private var value: ReaderCredentials?
    func load() -> ReaderCredentials? { loads += 1; return value }
    func save(_ c: ReaderCredentials) { value = c }
    func clear() { value = nil }
}
