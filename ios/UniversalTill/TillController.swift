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
    private var busy = false // only touched on `queue`
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

    /// "Back to till" on the error bar: load the start page again.
    func reloadHome() {
        reloadToken += 1
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
                if TillServer.isAnswering(url) { return }
                self.runOnQueue { try TillServer.restart() }
            }
        }
    }

    private func run(_ work: @escaping () throws -> URL) {
        queue.async { [weak self] in self?.runOnQueue(work) }
    }

    /// Must be called on `queue`; serialises start/restart so a resume
    /// can't race the first boot.
    private func runOnQueue(_ work: () throws -> URL) {
        if busy { return }
        busy = true
        defer { busy = false }
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
