import Foundation

/// The shell's whole state: is the server starting, running (at which
/// URL) or failed — plus a counter the WebView watches to know it must
/// reload after a restart.
final class TillController: ObservableObject {
    enum State: Equatable {
        case starting
        case running(URL)
        case failed(String)
    }

    @Published private(set) var state: State = .starting
    /// Bumped whenever the WebView must load the till's start page again.
    @Published private(set) var reloadToken = 0

    private let queue = DispatchQueue(label: "com.universaltill.pos.server")
    private var booted = false // only touched on the main thread

    /// First start. onAppear can fire more than once; only the first call
    /// starts anything, so a later one can't reload the page mid-sale.
    func boot() {
        if booted { return }
        booted = true
        run { try TillServer.start() }
    }

    /// Retry after a failed start (the Retry button).
    func retry() {
        state = .starting
        run { try TillServer.start() }
    }

    /// "Back to till" on the error bar: make sure the server is still up
    /// (it may have died while the app was in the foreground), then load
    /// the start page again.
    func reloadHome() {
        guard case .running(let url) = state else { return retry() }
        queue.async { [weak self] in
            guard let self = self else { return }
            if self.serverIsUp(url) {
                DispatchQueue.main.async { self.reloadToken += 1 }
            } else {
                self.runOnQueue { try TillServer.restart() }
            }
        }
    }

    /// Called whenever the app becomes active. iOS can reclaim a suspended
    /// app's listening socket (Apple TN2277), leaving a server that no
    /// longer answers; probe it and restart only if it's gone. Never
    /// restart a healthy server — that would drop a sale in progress.
    func resume() {
        switch state {
        case .starting:
            return // boot() is already on it
        case .failed:
            retry()
        case .running(let url):
            queue.async { [weak self] in
                guard let self = self else { return }
                if self.serverIsUp(url) { return }
                self.runOnQueue { try TillServer.restart() }
            }
        }
    }

    /// A false "down" would restart a healthy server and drop the sale in
    /// progress (the basket lives in the server's memory), so be slow to
    /// say no: the Go side's own view first — a reclaimed listening socket
    /// makes Serve return and IsRunning false — then up to three probes.
    /// Runs on `queue`.
    private func serverIsUp(_ url: URL) -> Bool {
        if !TillServer.isRunning() { return false }
        for attempt in 0..<3 {
            if TillServer.isAnswering(url) { return true }
            if attempt < 2 { Thread.sleep(forTimeInterval: 1) }
        }
        return false
    }

    private func run(_ work: @escaping () throws -> URL) {
        queue.async { [weak self] in self?.runOnQueue(work) }
    }

    /// Must be called on `queue`: the serial queue is what keeps a resume
    /// from racing the first boot.
    private func runOnQueue(_ work: () throws -> URL) {
        let result = Result { try work() }
        DispatchQueue.main.async {
            switch result {
            case .success(let url):
                self.state = .running(url)
                self.reloadToken += 1
            case .failure(let error):
                self.state = .failed(error.localizedDescription)
            }
        }
    }
}
