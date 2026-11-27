import XCTest

/// The foreground "ask the cloud about the credential" hook (ut-docs#7097 T8)
/// fires on every scene-active, and iOS flips the scene to active for a
/// notification-centre pull-down too. The throttle keeps that from turning
/// into a cloud request each time.
final class ForegroundThrottleTests: XCTestCase {
    var now: TimeInterval = 1_000
    var throttle: ForegroundThrottle!

    override func setUp() {
        super.setUp()
        throttle = ForegroundThrottle(minInterval: 30, now: { [unowned self] in self.now })
    }

    func testTheFirstForegroundAlwaysFires() {
        XCTAssertTrue(throttle.shouldFire())
    }

    func testASecondForegroundInsideTheIntervalIsSwallowed() {
        XCTAssertTrue(throttle.shouldFire())
        now += 29
        XCTAssertFalse(throttle.shouldFire())
    }

    func testItFiresAgainOnceTheIntervalHasPassed() {
        XCTAssertTrue(throttle.shouldFire())
        now += 30
        XCTAssertTrue(throttle.shouldFire(), "exactly the interval later counts")
    }

    func testASwallowedCallDoesNotPushTheWindowOut() {
        XCTAssertTrue(throttle.shouldFire())
        now += 20
        XCTAssertFalse(throttle.shouldFire())
        now += 10 // 30 s after the FIRST fire, 10 s after the swallowed one
        XCTAssertTrue(throttle.shouldFire())
    }

    func testAClockStepBackwardsFiresRatherThanStayingSilent() {
        XCTAssertTrue(throttle.shouldFire())
        now -= 3_600 // a manual clock change
        XCTAssertTrue(throttle.shouldFire())
    }

    func testResetLetsTheNextForegroundFireAtOnce() {
        XCTAssertTrue(throttle.shouldFire())
        throttle.reset() // the server was restarted: its engine knows nothing yet
        XCTAssertTrue(throttle.shouldFire())
    }
}
