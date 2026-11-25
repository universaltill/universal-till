// The reader's network: pinned HTTPS for pairing and the re-proof, and the
// pinned WebSocket to wss://<main>/api/sync/link (ADR-0114, ADR-0198 §6.3).
// The main is dialled by IP on the LAN: its identity is the pinned key, not a
// name. The pin check runs in the server-trust challenge, i.e. before any HTTP
// request bytes exist, so the reader's Bearer is never sent to an unpinned peer.
import Foundation
import Security

/// Server-trust handling shared by every connection here. `pin` nil = learning
/// (pairing's first contact: records the leaf pin so the manager can compare the code).
class PinnedDelegate: NSObject, URLSessionDelegate {
    let pin: String?
    private let lock = NSLock()
    private var _seen: String?
    private var _rejected = false
    var seenPin: String? { lock.lock(); defer { lock.unlock() }; return _seen }
    var pinRejected: Bool { lock.lock(); defer { lock.unlock() }; return _rejected }

    init(pin: String?) { self.pin = pin }

    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate]
        let leaf = chain?.first
        let seen = leaf.flatMap { SpkiPin.pin(ofCertificate: $0) }
        lock.lock(); _seen = seen; lock.unlock()
        if let pin {
            if SpkiPin.decide(leaf: leaf, pinned: pin) == .trust {
                completionHandler(.useCredential, URLCredential(trust: trust))
            } else {
                lock.lock(); _rejected = true; lock.unlock()
                completionHandler(.cancelAuthenticationChallenge, nil)
            }
        } else if seen != nil {
            completionHandler(.useCredential, URLCredential(trust: trust))
        } else {
            lock.lock(); _rejected = true; lock.unlock()
            completionHandler(.cancelAuthenticationChallenge, nil)   // not a P-256 key: fail closed
        }
    }
}

/// PairHttp over URLSession, synchronous (call it off the main thread). Redirects are never followed.
final class URLSessionPairHttp: PairHttp {
    private final class Delegate: PinnedDelegate, URLSessionDataDelegate, URLSessionTaskDelegate {
        func urlSession(_ s: URLSession, task: URLSessionTask, willPerformHTTPRedirection r: HTTPURLResponse,
                        newRequest: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
            completionHandler(nil)
        }
    }

    private func run(_ req: URLRequest, expectPin: String?) -> PairAnswer {
        let d = Delegate(pin: expectPin)
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 15
        cfg.urlCache = nil
        let session = URLSession(configuration: cfg, delegate: d, delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }
        let sem = DispatchSemaphore(value: 0)
        var response: HTTPURLResponse?
        var received = Data()
        // With a completion handler URLSession never calls the data-delivery delegate method, so the body
        // arrives here (an oversized body is cut below; the answer is a few hundred bytes at most).
        session.dataTask(with: req) { data, r, _ in
            response = r as? HTTPURLResponse
            received = data ?? Data()
            sem.signal()
        }.resume()
        sem.wait()
        guard let r = response else {
            return PairAnswer(status: d.pinRejected && expectPin != nil ? -2 : -1, body: "", pin: nil)
        }
        let text = String(decoding: received.prefix(PairHttpLimits.maxBody), as: UTF8.self)
        let retry = Int((r.value(forHTTPHeaderField: "Retry-After") ?? "").trimmingCharacters(in: .whitespaces)) ?? 0
        return PairAnswer(status: r.statusCode, body: text, pin: d.seenPin ?? expectPin, retryAfterSec: retry)
    }

    func post(_ url: String, body: String, expectPin: String?) -> PairAnswer {
        guard let u = URL(string: url) else { return PairAnswer(status: -1, body: "", pin: nil) }
        var r = URLRequest(url: u)
        r.httpMethod = "POST"
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = Data(body.utf8)
        return run(r, expectPin: expectPin)
    }

    func get(_ url: String, headers: [String: String], expectPin: String?) -> PairAnswer {
        guard let u = URL(string: url) else { return PairAnswer(status: -1, body: "", pin: nil) }
        var r = URLRequest(url: u)
        for (k, v) in headers { r.setValue(v, forHTTPHeaderField: k) }
        return run(r, expectPin: expectPin)
    }
}

/// The reader's link to the main. All state lives on `session.queue`. Reconnects
/// with a short back-off while the app wants a link; a 401 on the upgrade (or close
/// code 4003) means the main removed this phone and ends the loop.
final class ReaderLink {
    private let session: ReaderSession
    private let reProof: PinReProof?
    private let onAdopted: (ReaderCredentials) -> Void
    private var creds: ReaderCredentials
    private var wanted = false
    private var attempts = 0
    private var current: Connection?
    private var retry: DispatchWorkItem?

    init(creds: ReaderCredentials, session: ReaderSession, reProof: PinReProof?,
         onAdopted: @escaping (ReaderCredentials) -> Void) {
        self.creds = creds
        self.session = session
        self.reProof = reProof
        self.onAdopted = onAdopted
    }

    /// Any thread.
    func start() {
        session.queue.async {
            guard !self.wanted else { return }
            self.wanted = true
            self.connect()
        }
    }

    /// Any thread.
    func stop() {
        session.queue.async {
            self.wanted = false
            self.retry?.cancel(); self.retry = nil
            self.current?.shutdown(); self.current = nil
        }
    }

    // MARK: one connection

    private final class Connection: PinnedDelegate, URLSessionWebSocketDelegate, ReaderLinkTransport {
        var task: URLSessionWebSocketTask!
        var urlSession: URLSession!
        var onEnd: ((_ httpStatus: Int?, _ closeCode: Int?, _ retryAfter: Int?, _ pinMismatch: Bool) -> Void)?
        private var closeCode: Int?
        private var ended = false

        func send(_ text: String) -> Bool {
            guard task.state == .running else { return false }
            task.send(.string(text)) { _ in }
            return true
        }

        func close() { shutdown() }

        func shutdown() {
            onEnd = nil
            task?.cancel(with: .normalClosure, reason: nil)
            urlSession?.invalidateAndCancel()
        }

        func urlSession(_ s: URLSession, webSocketTask t: URLSessionWebSocketTask, didCloseWith code: URLSessionWebSocketTask.CloseCode,
                        reason: Data?) {
            closeCode = code.rawValue
            finish(error: nil)
        }

        func urlSession(_ s: URLSession, task t: URLSessionTask, didCompleteWithError error: Error?) {
            finish(error: error)
        }

        private func finish(error: Error?) {
            let r = task.response as? HTTPURLResponse
            let retryAfter = Int((r?.value(forHTTPHeaderField: "Retry-After") ?? "").trimmingCharacters(in: .whitespaces))
            let cb = onEnd
            onEnd = nil
            cb?(r?.statusCode, closeCode ?? (task.closeCode == .invalid ? nil : task.closeCode.rawValue), retryAfter, pinRejected)
        }
    }

    private func connect() {
        guard wanted, !session.isRevoked, let url = URL(string: "wss://\(creds.address)/api/sync/link") else { return }
        let conn = Connection(pin: creds.pin)
        var req = URLRequest(url: url)
        req.setValue("Bearer \(creds.bearer)", forHTTPHeaderField: "Authorization")
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 20
        conn.urlSession = URLSession(configuration: cfg, delegate: conn, delegateQueue: nil)
        conn.task = conn.urlSession.webSocketTask(with: req)
        let gen = session.attach(conn)
        current = conn
        conn.onEnd = { [weak self, weak conn] status, code, retryAfter, mismatch in
            guard let self else { return }
            self.session.queue.async {
                guard let conn, self.current === conn else { return }
                self.current = nil
                conn.urlSession.invalidateAndCancel()
                self.ended(gen: gen, authFailed: ReaderLinkClose.isRevoked(httpStatus: status) || ReaderLinkClose.isRevoked(closeCode: code),
                           retryAfter: retryAfter, pinMismatch: mismatch)
            }
        }
        receive(conn, gen: gen)
        conn.task.resume()
    }

    private func receive(_ conn: Connection, gen: Int) {
        conn.task.receive { [weak self, weak conn] result in
            guard let self, let conn else { return }
            self.session.queue.async {
                guard self.current === conn else { return }
                switch result {
                case .success(let msg):
                    self.attempts = 0
                    if case .string(let text) = msg { self.session.onText(gen: gen, text) }
                    self.receive(conn, gen: gen)
                case .failure:
                    break   // the delegate's completion reports why
                }
            }
        }
    }

    private func ended(gen: Int, authFailed: Bool, retryAfter: Int?, pinMismatch: Bool) {
        session.onClosed(gen: gen, authFailed: authFailed)
        guard wanted, !authFailed else { return }
        if pinMismatch, reProof != nil { reProve(); return }
        schedule(ReaderLinkClose.retryDelayMs(attempt: attempts, retryAfterSec: retryAfter))
        attempts += 1
    }

    private func schedule(_ ms: Int64) {
        let item = DispatchWorkItem { [weak self] in self?.retry = nil; self?.connect() }
        retry = item
        session.queue.asyncAfter(deadline: .now() + .milliseconds(Int(ms)), execute: item)
    }

    /// The main's key changed: never wipe, never send the bearer there; ask for the proof (one try per 30 s).
    private func reProve() {
        guard let rp = reProof else { return }
        let c = creds
        DispatchQueue.global(qos: .utility).async {
            let r = rp.attempt(c)
            self.session.queue.async {
                guard self.wanted else { return }
                if case .adopted(let next) = r {
                    self.creds = next
                    self.onAdopted(next)
                    self.attempts = 0
                    self.connect()
                } else {
                    // anything but a verified proof is unverified: keep the pairing, back off
                    self.schedule(ReaderLinkClose.retryDelayMs(attempt: self.attempts, retryAfterSec: 30))
                    self.attempts += 1
                }
            }
        }
    }
}
