import Foundation
import Mobile

/// Thin wrapper over the gomobile-bound Go package (universal-till/mobile).
/// Every call here can block for seconds (Start waits until the server
/// answers), so callers run them off the main thread.
enum TillServer {
    /// The till's on-device data directory: Application Support/till — the
    /// sandboxed, backed-up, never-user-visible location iOS intends for
    /// app data (mobile.Start's doc names it as the iOS choice).
    static func dataDirectory() throws -> String {
        let base = try FileManager.default.url(
            for: .applicationSupportDirectory, in: .userDomainMask,
            appropriateFor: nil, create: true)
        let dir = base.appendingPathComponent("till", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir.path
    }

    /// Starts the server (idempotent while it is running) and returns the
    /// loopback URL the WebView loads. That URL may serve the boot-failure
    /// recovery screen (ADR-0075) rather than the till — load it either way.
    static func start() throws -> URL {
        let dir = try dataDirectory()
        MobileSetDeviceModel(deviceModel())
        // Bonjour instead of Go's raw multicast, which iOS refuses without
        // Apple's multicast entitlement (ut-docs#3218).
        MobileSetDiscoveryBridge(BonjourBridge.shared)
        var err: NSError?
        let addr = MobileStart(dir, &err)
        if let err = err {
            throw err
        }
        guard !addr.isEmpty, let url = URL(string: "http://\(addr)/") else {
            throw NSError(domain: "UniversalTill", code: 1,
                          userInfo: [NSLocalizedDescriptionKey: NSLocalizedString("error_no_address", comment: "")])
        }
        return url
    }

    /// Stops the server and starts it again — used when iOS reclaimed the
    /// listening socket while the app was suspended (Apple TN2277).
    static func restart() throws -> URL {
        MobileStop()
        return try start()
    }

    /// The Go side's own view: false once app.Run has returned (e.g. the
    /// listener died after iOS reclaimed its socket).
    static func isRunning() -> Bool {
        MobileIsRunning()
    }

    /// An ephemeral session: never reuse a pooled keep-alive connection
    /// that died while the app was suspended.
    private static let probeSession = URLSession(configuration: .ephemeral)

    /// Whether the server behind `base` still answers: /healthz 200, or the
    /// recovery screen's 503 + X-UT-Mode: recovery (the same "ready" rule
    /// as mobile.waitUntilReady). Blocking, with a short timeout.
    static func isAnswering(_ base: URL) -> Bool {
        var request = URLRequest(url: base.appendingPathComponent("healthz"))
        request.timeoutInterval = 2
        request.cachePolicy = .reloadIgnoringLocalCacheData
        let done = DispatchSemaphore(value: 0)
        var ok = false
        probeSession.dataTask(with: request) { _, response, _ in
            if let http = response as? HTTPURLResponse {
                ok = http.statusCode == 200 ||
                    (http.statusCode == 503 && http.value(forHTTPHeaderField: "X-UT-Mode") == "recovery")
            }
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 3)
        return ok
    }

    /// The hardware model identifier ("iPad13,4"), the iOS counterpart of
    /// Android's Build.MODEL for the diagnostics inventory (ut-docs#2235).
    private static func deviceModel() -> String {
        var info = utsname()
        uname(&info)
        return withUnsafeBytes(of: &info.machine) { raw in
            String(decoding: raw.prefix(while: { $0 != 0 }), as: UTF8.self)
        }
    }
}
