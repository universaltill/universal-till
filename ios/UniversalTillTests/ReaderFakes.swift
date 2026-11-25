// Fakes for the reader-mode tests (ut-docs#6538): a link, a UI sink, scripted
// payments, a scripted HTTP for pairing, and the shared golden fixtures.
import Foundation
import XCTest

/// internal/fleetlink/testdata/reader_*.json, owned by the Go side and shared
/// with the Kotlin tests: the Swift tests read the SAME bytes.
enum Golden {
    static func url(_ name: String) -> URL {
        let bundle = Bundle(for: GoldenAnchor.self)
        if let u = bundle.url(forResource: "reader_\(name)", withExtension: "json", subdirectory: "testdata") { return u }
        fatalError("golden reader_\(name).json is not in the test bundle")
    }
    static func text(_ name: String) -> String {
        (try? String(contentsOf: url(name), encoding: .utf8))?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }
    static func json(_ name: String) -> [String: Any] {
        (try? JSONSerialization.jsonObject(with: Data(text(name).utf8))) as? [String: Any] ?? [:]
    }
    static func payload(_ name: String) -> [String: Any] { json(name)["payload"] as? [String: Any] ?? [:] }
    static func fixture(_ name: String) -> [String: Any] {
        guard let u = Bundle(for: GoldenAnchor.self).url(forResource: name, withExtension: "json"),
              let d = try? Data(contentsOf: u),
              let j = try? JSONSerialization.jsonObject(with: d) as? [String: Any] else { fatalError("fixture \(name)") }
        return j
    }
}
private final class GoldenAnchor {}

func jsonEqual(_ a: Any?, _ b: Any?) -> Bool {
    guard let a, let b else { return false }
    return (a as AnyObject).isEqual(b)
}

final class FakeTransport: ReaderLinkTransport {
    var sent: [String] = []
    var closed = false
    var writable = true
    func send(_ text: String) -> Bool {
        if !writable { return false }
        sent.append(text)
        return true
    }
    func close() { closed = true }

    func frames(_ type: String) -> [[String: Any]] {
        sent.compactMap { (try? JSONSerialization.jsonObject(with: Data($0.utf8))) as? [String: Any] }
            .filter { ($0["type"] as? String) == type }
    }
    func last(_ type: String) -> [String: Any]? { frames(type).last }
    func payload(_ type: String) -> [String: Any]? { last(type)?["payload"] as? [String: Any] }
}

final class FakeReaderUi: ReaderUi {
    var screens: [ReaderScreen] = []
    var incomingCount = 0
    var revokedCount = 0
    var refusedCount = 0
    var last: ReaderScreen? { screens.last }
    func show(_ s: ReaderScreen) { screens.append(s) }
    func incoming() { incomingCount += 1 }
    func revoked() { revokedCount += 1 }
    func refusedWhileBusy() { refusedCount += 1 }
}

final class FakeReaderPayments: ReaderPayments {
    var cap = Capability.of(.ready)
    var acceptStart = true
    var collects: [ReaderCollect] = []
    var pendingEnd: ((ReaderOutcome) -> Void)?
    var cancels: [String] = []
    var disconnects = 0
    var setups = 0
    var onTillBusyChange: ((Bool) -> Void)?

    func capability() -> Capability { cap }
    func setup(_ cb: @escaping (String?) -> Void) { setups += 1; cap = .of(.ready); cb(nil) }
    func collect(_ c: ReaderCollect, remainingMs: Int64, started: @escaping (Bool) -> Void,
                 end: @escaping (ReaderOutcome) -> Void) {
        collects.append(c)
        started(acceptStart)
        if acceptStart { pendingEnd = end }
    }
    func cancel(key: String) { cancels.append(key) }
    func disconnect() { disconnects += 1 }
    func finish(_ o: ReaderOutcome) { let e = pendingEnd; pendingEnd = nil; e?(o) }
}

/// Scripted HTTP: every request is recorded; answers come from the queue (or `fallback`).
final class FakeHttp: PairHttp {
    struct Request { var method: String; var url: String; var body: String; var headers: [String: String]; var expectPin: String? }
    var requests: [Request] = []
    var answers: [PairAnswer] = []
    var fallback = PairAnswer(status: -1, body: "", pin: nil)
    func next() -> PairAnswer { answers.isEmpty ? fallback : answers.removeFirst() }
    func post(_ url: String, body: String, expectPin: String?) -> PairAnswer {
        requests.append(Request(method: "POST", url: url, body: body, headers: [:], expectPin: expectPin))
        return next()
    }
    func get(_ url: String, headers: [String: String], expectPin: String?) -> PairAnswer {
        requests.append(Request(method: "GET", url: url, body: "", headers: headers, expectPin: expectPin))
        return next()
    }
}
