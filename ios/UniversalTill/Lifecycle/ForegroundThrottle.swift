import Foundation

/// Rate-limits the foreground credential check (ut-docs#7097 T8). The till
/// asks the cloud whether its credential still works every time the app comes
/// to the front, because a suspended app runs no timers; iOS also reports the
/// scene as active after a notification-centre pull-down, so repeat calls
/// inside `minInterval` are swallowed. Pure Swift on purpose: it is compiled
/// into the unit-test target, which has no Mobile.xcframework.
final class ForegroundThrottle {
    private let minInterval: TimeInterval
    private let now: () -> TimeInterval
    private var last: TimeInterval?
    private let lock = NSLock()

    /// `now` is seconds on a clock that need not be monotonic; tests pass a fake.
    init(minInterval: TimeInterval = 30,
         now: @escaping () -> TimeInterval = { Date().timeIntervalSince1970 }) {
        self.minInterval = minInterval
        self.now = now
    }

    /// True when the caller should notify the Go side now. A swallowed call
    /// does not move the window; a clock that went backwards fires.
    func shouldFire() -> Bool {
        lock.lock(); defer { lock.unlock() }
        let t = now()
        if let last = last, t >= last, t - last < minInterval { return false }
        last = t
        return true
    }

    /// Forget the last fire (the server was restarted: its engine is new).
    func reset() {
        lock.lock(); defer { lock.unlock() }
        last = nil
    }
}
