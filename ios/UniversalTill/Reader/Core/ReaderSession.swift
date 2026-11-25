// The phone's side of the reader link (ADR-0198 §6): a pure, queue-confined
// state machine (Swift port of Android's ReaderSession.kt). It speaks only
// after the main's hello, takes ONE collect at a time (a second is answered
// `busy`, nothing started), reports `reader.status` on every change, relays the
// SDK's connection-token request over the link, and answers each collect with
// exactly one `reader.result` on the SAME link the collect arrived on.
//
// The result is ADVICE: the main re-derives every fact from the payment
// service, so anything unsure is sent `pending`, never `declined`. If the link
// is gone when the verdict arrives it is dropped; the main already holds the
// payment pending (`link_lost`). A collect whose link stays down for
// `linkLossDropMs` is cancelled.
//
// Every method must be called on `queue` (the link and the payments hop onto it).
import Foundation

/// The link, as the session sees it (URLSession in the app, a fake in tests).
protocol ReaderLinkTransport: AnyObject {
    /// false = it could not be written (closed).
    func send(_ text: String) -> Bool
    func close()
}

enum ReaderLinkClose {
    /// Only a 401 says "the main removed this phone"; 403, 429, 503 and the rest are "try again later".
    static func isRevoked(httpStatus: Int?) -> Bool { httpStatus == 401 }
    /// The main closes a revoked reader's socket with 4003 (ADR-0198 §6.2).
    static func isRevoked(closeCode: Int?) -> Bool { closeCode == 4003 }
    /// Back-off 1..15 s, or the main's Retry-After (bounded to 2 min).
    static func retryDelayMs(attempt: Int, retryAfterSec: Int?) -> Int64 {
        if let r = retryAfterSec, r > 0 { return min(120_000, Int64(r) * 1_000) }
        return min(15_000, Int64(1_000) << Int64(min(max(attempt, 0), 4)))
    }
}

enum ReaderResultKind: Equatable { case done, cancelled, cardRefused, expired, pending, linkLost }

/// What the phone shows. Never an amount outside `pay`, never card data.
enum ReaderScreen: Equatable {
    case connecting, idle, offline, needsSetup, paused, revoked
    case pay(amountMinor: Int64, currency: String, timeoutMs: Int64)
    case result(ReaderResultKind)
}

protocol ReaderUi: AnyObject {
    func show(_ s: ReaderScreen)
    /// The vibration that says a payment is requested.
    func incoming()
    /// The main removed this phone: the app clears its credentials and returns to enrolment.
    func revoked()
    /// A request from the main was refused because this phone was busy (S5).
    func refusedWhileBusy()
}

enum TokenResult: Equatable, CustomStringConvertible {
    case ok(secret: String, locationId: String)
    case err(String)
    var description: String {
        switch self {
        case .ok(_, let l): return "ok(location=\(l))"
        case .err(let c): return "err(\(c))"
        }
    }
}

final class ReaderSession: ReaderTokenSource {
    static let pingMs: Int64 = 5_000
    static let linkLossDropMs: Int64 = 30_000
    static let resultShownMs: Int64 = 4_000
    static let tokenTimeoutMs: Int64 = 10_000

    private final class Live {
        let key: String
        let gen: Int
        var accepted = false
        init(key: String, gen: Int) { self.key = key; self.gen = gen }
    }

    private final class TokenWait {
        let cb: (TokenResult) -> Void
        var timer: TimerToken?
        init(_ cb: @escaping (TokenResult) -> Void) { self.cb = cb }
    }

    let queue: DispatchQueue
    private let clock: TapToPayClock
    private let payments: ReaderPayments
    private let ui: ReaderUi
    private let appVersion: String

    private var transport: ReaderLinkTransport?
    private var gen = 0
    private var helloed = false
    private var foreground = false
    private var tillBusy = false
    private var live: Live?
    private var seq = 0
    private var pingTimer: TimerToken?
    private var lossTimer: TimerToken?
    private var resultTimer: TimerToken?
    private var tokens: [String: TokenWait] = [:]
    private(set) var isRevoked = false

    /// A collect is in flight: the app must not drop the link when it goes to
    /// the background (Apple's sheet makes it inactive; the SDK keeps running).
    var hasLive: Bool { live != nil }
    var isLinked: Bool { helloed && transport != nil }

    init(queue: DispatchQueue, clock: TapToPayClock, payments: ReaderPayments, ui: ReaderUi, appVersion: String) {
        self.queue = queue
        self.clock = clock
        self.payments = payments
        self.ui = ui
        self.appVersion = appVersion
        payments.onTillBusyChange = { [weak self] busy in self?.tillBusyChanged(busy) }
    }

    private func nextId() -> String { seq += 1; return "p-\(seq)" }

    private func after(_ ms: Int64, _ block: @escaping () -> Void) -> TimerToken {
        clock.schedule(atMs: clock.nowMs() + ms, on: queue, block)
    }

    // MARK: link lifecycle

    /// A new link (the old one, if any, is closed). Nothing is sent until the main speaks first.
    @discardableResult
    func attach(_ t: ReaderLinkTransport) -> Int {
        let old = transport
        transport = t
        gen += 1
        helloed = false
        pingTimer?.invalidate(); pingTimer = nil
        // The 30 s loss clock is NOT touched here: a reconnect attempt is not contact.
        old?.close()
        failTokens("offline")
        if live == nil { ui.show(.connecting) }
        return gen
    }

    func onForeground() {
        foreground = true
        if helloed { reportStatus() }
        if live == nil && resultTimer == nil { showIdle() }
    }

    func onBackground() {
        foreground = false
        // Backgrounded and idle: no token-holding Terminal session stays open (ADR-0198 §6.4).
        if live == nil { payments.disconnect() }
        if helloed { reportStatus() }
        if live == nil && resultTimer == nil { ui.show(.paused) }
    }

    func onClosed(gen g: Int, authFailed: Bool) {
        guard g == gen else { return }
        helloed = false
        pingTimer?.invalidate(); pingTimer = nil
        transport = nil
        failTokens("offline")
        if authFailed {
            isRevoked = true
            lossTimer?.invalidate(); lossTimer = nil
            if let l = live { payments.cancel(key: l.key) }
            live = nil
            payments.disconnect()
            ui.revoked()
            ui.show(.revoked)
            return
        }
        if let l = live {
            // Anchored to the FIRST loss: back-off attempts that fail again do not restart it.
            if lossTimer == nil {
                lossTimer = after(Self.linkLossDropMs) { [weak self] in
                    guard let self else { return }
                    self.lossTimer = nil
                    if self.live === l {
                        self.payments.cancel(key: l.key)
                        self.ui.show(.result(.linkLost))
                    }
                }
            }
        } else if resultTimer == nil {
            ui.show(.offline)
        }
    }

    /// A fresh enrolment succeeded: the revoked state (and its stale link) of an earlier one is gone.
    func enrolled() {
        isRevoked = false
        helloed = false
        pingTimer?.invalidate(); pingTimer = nil
        lossTimer?.invalidate(); lossTimer = nil
        transport?.close()
        transport = nil
        gen += 1
    }

    /// The cashier's Cancel on the phone itself: stops the live collect (the verdict then goes out as cancelled).
    func cancelLive() {
        if let l = live { payments.cancel(key: l.key) }
    }

    /// The "Set up this phone" button: permission, terms, connect. Re-reports either way.
    func setup() {
        payments.setup { [weak self] _ in
            guard let self else { return }
            if self.helloed { self.reportStatus() }
            if self.live == nil && self.resultTimer == nil { self.showIdle() }
        }
    }

    private func tillBusyChanged(_ busy: Bool) {
        tillBusy = busy
        if helloed { reportStatus() }
    }

    // MARK: frames

    func onText(gen g: Int, _ text: String) {
        guard g == gen, !isRevoked, let e = ReaderFrames.parse(text) else { return }
        if e.type == "hello" { onHello(e); return }
        guard helloed else { return }
        switch e.type {
        case "reader.collect":
            onCollect(e)
        case "reader.cancel":
            if let key = ReaderFrames.parseCancelKey(e.payload), live?.key == key { payments.cancel(key: key) }
        case "reader.token", "error":
            onTokenAnswer(e)
        default:
            break
        }
    }

    private func onHello(_ e: ReaderEnvelope) {
        if helloed { return }
        let p = e.payload
        guard (p["role"] as? String) == "main", (p["reader_protocol"] as? NSNumber)?.intValue == ReaderFrames.protocolVersion else {
            transport?.close()
            return
        }
        helloed = true
        lossTimer?.invalidate(); lossTimer = nil   // real contact: the loss clock ends
        _ = send("hello", ReaderFrames.helloPayload(appVersion: appVersion))
        reportStatus()
        schedulePing()
        if live == nil && resultTimer == nil { showIdle() }
    }

    private func schedulePing() {
        pingTimer?.invalidate()
        pingTimer = after(Self.pingMs) { [weak self] in
            guard let self, self.helloed else { return }
            _ = self.send("ping", [:])
            self.schedulePing()
        }
    }

    @discardableResult
    private func send(_ type: String, _ payload: [String: Any], replyTo: String? = nil, id: String? = nil) -> Bool {
        transport?.send(ReaderFrames.encode(id: id ?? nextId(), type: type, payload: payload, replyTo: replyTo)) ?? false
    }

    private func cap() -> Capability { payments.capability() }

    private func reportStatus() {
        let c = cap()
        let s: ReaderStatus
        if live != nil || tillBusy { s = .busy }
        else if !foreground { s = .unavailable }
        else if !c.ready { s = .needsSetup }
        else { s = .ready }
        send("reader.status", ReaderFrames.statusPayload(s, reason: s == .needsSetup ? "reader_needs_setup" : nil))
    }

    private func showIdle() {
        if isRevoked { ui.show(.revoked) }
        else if !foreground { ui.show(.paused) }
        else if !helloed { ui.show(.offline) }
        else if !cap().ready { ui.show(.needsSetup) }
        else { ui.show(.idle) }
    }

    // MARK: collect

    private func onCollect(_ e: ReaderEnvelope) {
        guard let c = ReaderFrames.parseCollect(e.payload) else {
            send("error", ReaderFrames.errorPayload(code: "bad_message", retryable: false), replyTo: e.id)
            return
        }
        if live != nil || tillBusy || !foreground {
            send("error", ReaderFrames.errorPayload(code: "busy", retryable: true), replyTo: e.id)
            if live != nil || tillBusy { ui.refusedWhileBusy() }
            return
        }
        if !cap().ready {
            send("error", ReaderFrames.errorPayload(code: "not_ready", retryable: true), replyTo: e.id)
            return
        }
        let l = Live(key: c.key, gen: gen)
        live = l
        resultTimer?.invalidate(); resultTimer = nil
        payments.collect(c, remainingMs: c.timeoutMs, started: { [weak self] ok in
            guard let self, self.live === l else { return }
            if !ok {
                // The coordinator's lock was taken first (S5 race): the arbiter said busy, nothing started.
                self.live = nil
                self.send("error", ReaderFrames.errorPayload(code: "busy", retryable: true), replyTo: e.id)
                self.ui.refusedWhileBusy()
                if self.helloed { self.reportStatus() }
                return
            }
            l.accepted = true
            self.send("reader.collect", ReaderFrames.ackPayload(), replyTo: e.id)
            self.reportStatus()
            self.ui.show(.pay(amountMinor: c.amountMinor, currency: c.currency, timeoutMs: c.timeoutMs))
            self.ui.incoming()
        }, end: { [weak self] outcome in self?.onEnd(l, outcome) })
    }

    private func onEnd(_ l: Live, _ outcome: ReaderOutcome) {
        guard live === l else { return }
        live = nil
        lossTimer?.invalidate(); lossTimer = nil
        let result: ReaderResult
        let kind: ReaderResultKind
        switch outcome {
        case .notStarted:
            result = .cancelled; kind = .expired
        case .result(let r):
            result = r
            switch r {
            case .approved: kind = .done
            case .declined: kind = .cardRefused
            case .cancelled: kind = .cancelled
            case .pending: kind = .pending
            }
        }
        let sameLink = l.gen == gen && helloed
        if sameLink {
            send("reader.result", ReaderFrames.resultPayload(key: l.key, result))
            reportStatus()
        }
        ui.show(.result(sameLink ? kind : .linkLost))
        resultTimer?.invalidate()
        resultTimer = after(Self.resultShownMs) { [weak self] in
            guard let self else { return }
            self.resultTimer = nil
            if self.live == nil { self.showIdle() }
        }
    }

    // MARK: the SDK's connection token provider asks the main (reader.token)

    /// `done` runs once, on the session queue: the token, or a code (offline, timeout, not_ready, rate_limited, ...).
    func fetchToken(_ done: @escaping (TokenResult) -> Void) {
        guard helloed, transport != nil else { done(.err("offline")); return }
        let id = nextId()
        let w = TokenWait(done)
        tokens[id] = w
        w.timer = after(Self.tokenTimeoutMs) { [weak self] in self?.finishToken(id, .err("timeout")) }
        if !send("reader.token", ReaderFrames.tokenRequestPayload(), id: id) { finishToken(id, .err("offline")) }
    }

    private func finishToken(_ id: String, _ r: TokenResult) {
        guard let w = tokens.removeValue(forKey: id) else { return }
        w.timer?.invalidate()
        w.cb(r)
    }

    private func failTokens(_ code: String) {
        for id in Array(tokens.keys) { finishToken(id, .err(code)) }
    }

    private func onTokenAnswer(_ e: ReaderEnvelope) {
        guard let id = e.replyTo, tokens[id] != nil else { return }
        if e.type == "error" {
            let code = (e.payload["code"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? "unavailable"
            finishToken(id, .err(code))
            return
        }
        guard let t = ReaderFrames.parseToken(e.payload) else { finishToken(id, .err("bad_reply")); return }
        finishToken(id, .ok(secret: t.secret, locationId: t.locationId))
    }
}
