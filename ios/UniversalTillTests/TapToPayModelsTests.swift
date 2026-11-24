// The build switch and the JSON shapes shared with internal/devicepay
// (ut-docs#3049): the same golden files the Go test decodes strictly.
import XCTest

final class TapToPayModelsTests: XCTestCase {
    func testFeatureFlagIsOnlyTheExactStringYES() {
        XCTAssertTrue(TapToPayFeature.isEnabled(info: ["UTTapToPayEnabled": "YES"]))
        for v: Any in ["NO", "yes", "Yes", "1", "true", "", "$(UT_TAP_TO_PAY)", true, 1] {
            XCTAssertFalse(TapToPayFeature.isEnabled(info: ["UTTapToPayEnabled": v]), "\(v)")
        }
        XCTAssertFalse(TapToPayFeature.isEnabled(info: [:]))
        XCTAssertFalse(TapToPayFeature.isEnabled(info: nil))
        XCTAssertTrue(TapToPayFeature.isSimulated(info: ["UTTapToPaySimulated": "YES"]))
        XCTAssertFalse(TapToPayFeature.isSimulated(info: ["UTTapToPaySimulated": "NO"]))
    }

    /// internal/devicepay/testdata — the Go side decodes these strictly.
    func golden(_ name: String) throws -> Data {
        let repo = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // UniversalTillTests
            .deletingLastPathComponent() // ios
            .deletingLastPathComponent() // repo root
        return try Data(contentsOf: repo.appendingPathComponent("internal/devicepay/testdata/\(name)"))
    }

    func testGoldenRequestDecodes() throws {
        let req = try JSONDecoder().decode(CollectRequest.self, from: golden("request_golden.json"))
        XCTAssertEqual(req, CollectRequest(provider: "stripe", amountMinor: 1000, currency: "gbp",
                                           idempotencyKey: "sale-42-try-1", description: "Sale 42",
                                           deadlineUnixMs: 1_763_300_000_000))
    }

    func testGoldenResultRoundTrip() throws {
        let data = try golden("result_golden.json")
        let decoded = try JSONDecoder().decode(CollectResult.self, from: data)
        XCTAssertEqual(decoded, CollectResult(status: .approved, providerTxnId: "pi_123", chargeId: "ch_123",
                                              cardBrand: "visa", last4: "4242"))
        let encoded = try JSONEncoder().encode(decoded)
        let a = try JSONSerialization.jsonObject(with: encoded) as? NSDictionary
        let b = try JSONSerialization.jsonObject(with: data) as? NSDictionary
        XCTAssertEqual(a, b, "Swift must encode exactly the keys Go accepts")
    }

    /// ut-docs#6981: the Setup answer's optional raw error code.
    func testGoldenSetupRoundTrip() throws {
        let data = try golden("setup_golden.json")
        let decoded = try JSONDecoder().decode(SetupOutcome.self, from: data)
        XCTAssertEqual(decoded, SetupOutcome(ok: false, reason: "other", detail: "stripe 2900"))
        let encoded = try JSONEncoder().encode(decoded)
        let a = try JSONSerialization.jsonObject(with: encoded) as? NSDictionary
        let b = try JSONSerialization.jsonObject(with: data) as? NSDictionary
        XCTAssertEqual(a, b, "Swift must encode exactly the keys Go accepts")

        let ok = try JSONEncoder().encode(SetupOutcome(ok: true, reason: nil))
        let obj = try JSONSerialization.jsonObject(with: ok) as? [String: Any]
        XCTAssertEqual(Set(obj?.keys.map { $0 } ?? []), ["ok"])
    }

    func testNilFieldsAreOmitted() throws {
        let encoded = try JSONEncoder().encode(CollectResult.pending(TapToPayReason.timeout))
        let obj = try JSONSerialization.jsonObject(with: encoded) as? [String: Any]
        XCTAssertEqual(Set(obj?.keys.map { $0 } ?? []), ["status", "reason"])
    }
}
