import XCTest

final class ReaderPairingTests: XCTestCase {
    var http: FakeHttp!
    var now: Int64 = 1_000_000
    var pairing: ReaderPairing!
    let pin = String(repeating: "ab", count: 32)

    override func setUp() {
        super.setUp()
        http = FakeHttp()
        pairing = ReaderPairing(http: http, rand: { "secret" }, nowMs: { [unowned self] in self.now })
    }

    private func ok(_ data: [String: Any], pin: String? = nil) -> PairAnswer {
        .init(status: 200, body: ReaderFrames.serialise(["data": data, "error": NSNull()]), pin: pin)
    }

    private func started() { http.answers = [ok(["id": "req1"], pin: pin)]; _ = pairing.start(address: "192.0.2.20:8443", tillId: "main-1", deviceName: "Phone") }

    func testCommitmentIsSha256OfTheSecretAndTheRequestIsRoleCardReader() throws {
        started()
        let r = try XCTUnwrap(http.requests.first)
        XCTAssertEqual(r.url, "https://192.0.2.20:8443/api/sync/pair-request")
        let body = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(r.body.utf8)) as? [String: Any])
        XCTAssertEqual(body["commitment"] as? String, "2bb80d537b1da3e38bd30361aa855686bde0eacd7162fef6a25fe97bf527a25b", "sha256(\"secret\")")
        XCTAssertEqual(body["role"] as? String, "card_reader")
        XCTAssertEqual(body["device_name"] as? String, "Phone")
        XCTAssertNil(body["secret"], "the secret never leaves the phone")
    }

    func testTheCodeMatchesTheGoVector() throws {
        let v = Golden.fixture("pin_vector")["code"] as? [String: String] ?? [:]
        XCTAssertEqual(ReaderPairing.code(commitment: v["commitment"]!, tillId: v["till_id"]!, pin: v["pin"]!), v["code"])
        XCTAssertEqual(v["code"]?.count, 6)
    }

    func testStartReturnsTheCodeMadeOverTheSeenPin() {
        http.answers = [ok(["id": "req1"], pin: pin)]
        let s = pairing.start(address: "https://192.0.2.20:8443/", tillId: "main-1", deviceName: "Phone")
        XCTAssertEqual(s, .waiting(code: ReaderPairing.code(commitment: ReaderCrypto.sha256Hex("secret"), tillId: "main-1", pin: pin)))
    }

    func testPlainHttpIsRefusedBeforeAnyRequest() {
        let s = pairing.start(address: "http://192.0.2.20:8080", tillId: "main-1", deviceName: "Phone")
        XCTAssertEqual(s, .failed(reason: "needs_tls"))
        XCTAssertTrue(http.requests.isEmpty)
    }

    func testStartFailures() {
        http.answers = [.init(status: -1, body: "", pin: nil)]
        XCTAssertEqual(pairing.start(address: "h:1", tillId: "t", deviceName: "p"), .failed(reason: "not_found"))
        http.answers = [.init(status: 400, body: #"{"error":"tls_required"}"#, pin: nil)]
        XCTAssertEqual(pairing.start(address: "h:1", tillId: "t", deviceName: "p"), .failed(reason: "needs_tls"))
        http.answers = [.init(status: 403, body: "{}", pin: pin)]
        XCTAssertEqual(pairing.start(address: "h:1", tillId: "t", deviceName: "p"), .failed(reason: "refused"))
        http.answers = [ok(["id": "r"], pin: nil)]
        XCTAssertEqual(pairing.start(address: "h:1", tillId: "t", deviceName: "p"), .failed(reason: "needs_tls"))
        XCTAssertEqual(pairing.start(address: "h:1", tillId: "", deviceName: "p"), .failed(reason: "not_found"), "no till id, no code")
    }

    func testPollSendsTheSecretHeaderAndMeetsTheSamePin() {
        started()
        http.answers = [.init(status: 404, body: "", pin: nil)]
        XCTAssertEqual(pairing.poll(), .waiting)
        let r = http.requests.last
        XCTAssertEqual(r?.headers[ReaderPairing.secretHeader], "secret")
        XCTAssertEqual(r?.expectPin, pin)
        XCTAssertEqual(r?.url, "https://192.0.2.20:8443/api/sync/pair-requests/req1")
    }

    func testApprovedCarriesTheCredentialsAndKeepsTheSeenPin() {
        started()
        http.answers = [ok(["reader_id": "rd_1", "bearer": "b", "pin": pin])]
        XCTAssertEqual(pairing.poll(), .approved(ReaderCredentials(address: "192.0.2.20:8443", readerId: "rd_1", bearer: "b", pin: pin)))
    }

    func testApprovedWithAnotherPinIsRefused() {
        started()
        http.answers = [ok(["reader_id": "rd_1", "bearer": "b", "pin": "other"])]
        XCTAssertEqual(pairing.poll(), .failed(reason: "refused"))
        http.answers = [ok(["reader_id": "", "bearer": "b", "pin": pin])]
        XCTAssertEqual(pairing.poll(), .failed(reason: "refused"))
    }

    func testDeniedAndExpiredFrom410() {
        started()
        http.answers = [.init(status: 410, body: #"{"error":"pairing_denied"}"#, pin: nil)]
        XCTAssertEqual(pairing.poll(), .denied)
        http.answers = [.init(status: 410, body: #"{"error":"pairing_expired"}"#, pin: nil)]
        XCTAssertEqual(pairing.poll(), .expired)
        http.answers = [.init(status: 410, body: "{}", pin: nil)]
        XCTAssertEqual(pairing.poll(), .failed(reason: "refused"))
    }

    func testTenMinutesExpireTheRequestWithoutAnotherCall() {
        started()
        now += 10 * 60_000 + 1
        let before = http.requests.count
        XCTAssertEqual(pairing.poll(), .expired)
        XCTAssertEqual(http.requests.count, before)
    }

    func testBusyMainIsLaterNeverFailed() {
        started()
        http.answers = [.init(status: 429, body: "", pin: nil, retryAfterSec: 5)]
        XCTAssertEqual(pairing.poll(), .later(afterMs: 5_000))
        http.answers = [.init(status: 503, body: "", pin: nil)]
        XCTAssertEqual(pairing.poll(), .later(afterMs: 2_000))
    }

    func testAnotherCertificateOnPollIsFailed() {
        started()
        http.answers = [.init(status: -2, body: "", pin: nil)]
        XCTAssertEqual(pairing.poll(), .failed(reason: "refused"))
    }

    func testHostPortStripsSchemeAndSlash() {
        XCTAssertEqual(ReaderPairing.hostPort(" https://10.0.0.5:8443/ "), "10.0.0.5:8443")
        XCTAssertEqual(ReaderPairing.hostPort("10.0.0.5"), "10.0.0.5")
    }
}
