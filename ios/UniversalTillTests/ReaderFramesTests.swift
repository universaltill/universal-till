// The reader frames against the shared goldens (internal/fleetlink/testdata).
import XCTest

final class ReaderFramesTests: XCTestCase {
    private func p(_ name: String) -> [String: Any] { Golden.payload(name) }

    func testMainHelloGoldenParses() throws {
        let e = try XCTUnwrap(ReaderFrames.parse(Golden.text("hello_main")))
        XCTAssertEqual(e.type, "hello")
        XCTAssertEqual(e.id, "m1-1")
        XCTAssertEqual(e.payload["role"] as? String, "main")
        XCTAssertEqual(e.payload["reader_id"] as? String, "3f2a9c1e-0b7d-4e55-9a41-6d2f8c7b1a10")
    }

    func testRotatingHelloStillParses() throws {
        let e = try XCTUnwrap(ReaderFrames.parse(Golden.text("hello_main_rotating")))
        XCTAssertNotNil(e.payload["next_pin"])
    }

    func testPhoneHelloIsTheGoldenExceptPlatform() {
        var want = Golden.payload("hello_phone")
        want["platform"] = "ios"
        XCTAssertTrue(jsonEqual(ReaderFrames.helloPayload(appVersion: "0.36.2"), want))
    }

    func testStatusFramesEqualGoldens() {
        XCTAssertTrue(jsonEqual(ReaderFrames.statusPayload(.ready, reason: nil), p("status_ready")))
        XCTAssertTrue(jsonEqual(ReaderFrames.statusPayload(.needsSetup, reason: "reader_needs_setup"), p("status_needs_setup")))
    }

    func testStatusReasonOutsideTheClosedSetIsNeverSent() {
        let s = ReaderFrames.statusPayload(.needsSetup, reason: "card 4242")
        XCTAssertNil(s["reason"])
        XCTAssertEqual(s["state"] as? String, "needs_setup")
    }

    func testTokenRequestIsEmptyAndReplyParses() throws {
        XCTAssertTrue(ReaderFrames.tokenRequestPayload().isEmpty)
        let e = try XCTUnwrap(ReaderFrames.parse(Golden.text("token_reply")))
        XCTAssertEqual(e.replyTo, "p1-4")
        let t = try XCTUnwrap(ReaderFrames.parseToken(e.payload))
        XCTAssertEqual(t.secret, "pst_test_example_secret")
        XCTAssertEqual(t.locationId, "tml_example")
        XCTAssertFalse("\(t)".contains("pst_test"), "the token secret never reaches a log line")
    }

    func testTokenReplyWithoutSecretOrLocationIsRefused() {
        XCTAssertNil(ReaderFrames.parseToken(["location_id": "tml_x"]))
        XCTAssertNil(ReaderFrames.parseToken(["secret": "s"]))
        XCTAssertNil(ReaderFrames.parseToken(["secret": "", "location_id": "l"]))
    }

    func testCollectGoldenParsesToItsValues() throws {
        let c = try XCTUnwrap(ReaderFrames.parseCollect(p("collect")))
        XCTAssertEqual(c.amountMinor, 1250)
        XCTAssertEqual(c.currency, "gbp")
        XCTAssertEqual(c.key, "att_7:1:1")
        XCTAssertEqual(c.timeoutMs, 110_000)
        XCTAssertEqual(c.locationId, "tml_example")
        XCTAssertFalse("\(c)".contains("pi_example_secret"), "the client secret never reaches a log line")
    }

    func testCollectWithABadFieldIsRefused() {
        func with(_ k: String, _ v: Any) -> [String: Any] { var d = p("collect"); d[k] = v; return d }
        XCTAssertNil(ReaderFrames.parseCollect(with("amount_minor", 0)))
        XCTAssertNil(ReaderFrames.parseCollect(with("amount_minor", 10_000_000)))
        XCTAssertNil(ReaderFrames.parseCollect(with("amount_minor", 12.5)))
        XCTAssertNil(ReaderFrames.parseCollect(with("amount_minor", true)))
        XCTAssertNil(ReaderFrames.parseCollect(with("currency", "gbpx")))
        XCTAssertNil(ReaderFrames.parseCollect(with("idempotency_key", "a b")))
        XCTAssertNil(ReaderFrames.parseCollect(with("provider", "sumup")))
        XCTAssertNil(ReaderFrames.parseCollect(with("timeout_ms", 600_001)))
        XCTAssertNil(ReaderFrames.parseCollect(with("client_secret", "")))
        XCTAssertNil(ReaderFrames.parseCollect(with("location_id", "")))
    }

    func testAckAndBusyGoldens() {
        XCTAssertTrue(jsonEqual(ReaderFrames.ackPayload(), p("collect_ack")))
        XCTAssertTrue(jsonEqual(ReaderFrames.errorPayload(code: "busy", retryable: true), p("collect_busy")))
    }

    func testCancelGoldenParsesToItsKey() {
        XCTAssertEqual(ReaderFrames.parseCancelKey(p("cancel")), "att_7:1:1")
        XCTAssertNil(ReaderFrames.parseCancelKey(["idempotency_key": "a b"]))
    }

    func testResultFramesEqualGoldensAndCarryNoOtherField() {
        XCTAssertTrue(jsonEqual(ReaderFrames.resultPayload(key: "att_7:1:1", .approved), p("result_approved")))
        XCTAssertTrue(jsonEqual(ReaderFrames.resultPayload(key: "att_7:1:1", .declined(code: "card_declined")), p("result_declined")))
        XCTAssertTrue(jsonEqual(ReaderFrames.resultPayload(key: "att_7:1:1", .pending(reason: "amount_mismatch")), p("result_pending")))
    }

    func testDeclineCodeOrReasonOutsideTheSlugShapeIsDropped() {
        XCTAssertNil(ReaderFrames.resultPayload(key: "k", .declined(code: "Card Declined!"))["decline_code"])
        XCTAssertNil(ReaderFrames.resultPayload(key: "k", .pending(reason: "has space"))["reason"])
    }

    func testEncodedFrameRoundTripsThroughParse() throws {
        let text = ReaderFrames.encode(id: "p-1", type: "reader.status", payload: ["state": "ready"], replyTo: "m1-9")
        let e = try XCTUnwrap(ReaderFrames.parse(text))
        XCTAssertEqual(e.id, "p-1")
        XCTAssertEqual(e.replyTo, "m1-9")
    }

    func testParseIsStrictAboutTheEnvelope() {
        XCTAssertNil(ReaderFrames.parse("not json"))
        XCTAssertNil(ReaderFrames.parse(#"{"v":2,"id":"a","type":"x","payload":{}}"#))
        XCTAssertNil(ReaderFrames.parse(#"{"v":1,"id":"","type":"x"}"#))
        XCTAssertNil(ReaderFrames.parse(#"{"v":1,"id":"a","type":"x","payload":[]}"#))
        XCTAssertNil(ReaderFrames.parse(#"{"v":1,"id":"a","type":"x","extra":1}"#), "an unknown top-level key is dropped, as Go's decoder does")
        XCTAssertNil(ReaderFrames.parse(#"{"v":true,"id":"a","type":"x"}"#))
        XCTAssertNil(ReaderFrames.parse(String(repeating: "a", count: 17_000)))
        XCTAssertNotNil(ReaderFrames.parse(#"{"v":1,"id":"a","type":"x"}"#))
    }
}
