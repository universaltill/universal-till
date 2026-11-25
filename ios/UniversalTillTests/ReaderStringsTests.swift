// The reader_* strings: English only in this PR (Weblate later), and the
// wording rule of the Android ReaderGateTest (never "declined" / "charged" for
// anything unsure), plus every key the code references exists.
import XCTest

final class ReaderStringsTests: XCTestCase {
    private let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("UniversalTill")

    private func strings() throws -> [String: String] {
        let url = root.appendingPathComponent("en.lproj/Localizable.strings")
        let d = try XCTUnwrap(NSDictionary(contentsOf: url) as? [String: String])
        return d.filter { $0.key.hasPrefix("reader_") }
    }

    func testReaderStringsExist() throws {
        XCTAssertGreaterThan(try strings().count, 20)
    }

    func testNoReaderStringSaysDeclinedExceptTheCardNotAcceptedException() throws {
        for (k, v) in try strings() where v.lowercased().contains("declined") {
            XCTFail("\(k) says declined: \(v)")
        }
    }

    func testNoReaderStringSaysChargedExceptTheUnsureOnes() throws {
        let allowed: Set<String> = ["reader_result_lost", "reader_toast_refused", "reader_toast_refused_unnamed", "reader_result_pending"]
        for (k, v) in try strings() where v.lowercased().contains("charged") && !allowed.contains(k) {
            XCTFail("\(k) says charged: \(v)")
        }
        XCTAssertTrue(try XCTUnwrap(strings()["reader_result_lost"]).contains("Ask the cashier"))
    }

    func testOtherLanguagesAreNotTouchedByThisChange() throws {
        for lang in ["ar", "fa", "tr"] {
            let url = root.appendingPathComponent("\(lang).lproj/Localizable.strings")
            if let d = NSDictionary(contentsOf: url) as? [String: String] {
                XCTAssertTrue(d.keys.filter { $0.hasPrefix("reader_") }.isEmpty, "\(lang) must come from Weblate")
            }
        }
    }

    func testEveryKeyUsedInCodeExists() throws {
        let have = Set(try strings().keys)
        let dir = root.appendingPathComponent("Reader")
        let files = FileManager.default.enumerator(at: dir, includingPropertiesForKeys: nil)!.compactMap { $0 as? URL }.filter { $0.pathExtension == "swift" }
        let re = try NSRegularExpression(pattern: #"(?:rs|Text|Button|Label)\("(reader_[a-z0-9_]+)""#)
        var used = Set<String>()
        for f in files {
            let t = try String(contentsOf: f, encoding: .utf8)
            for m in re.matches(in: t, range: NSRange(t.startIndex..., in: t)) { used.insert(String(t[Range(m.range(at: 1), in: t)!])) }
        }
        XCTAssertFalse(used.isEmpty)
        XCTAssertEqual(used.subtracting(have), [], "keys used in code but missing from Localizable.strings")
    }
}
