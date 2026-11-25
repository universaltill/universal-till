// What the reader UI shows, on the main thread. The ReaderController feeds it
// (screen from the session, enrolment progress from the pairing loop); the
// views only read it. Strings come from en.lproj/Localizable.strings (reader_*).
import SwiftUI
import UIKit

/// Localised string lookup for the reader views (the strings test greps `rs("reader_…")`).
func rs(_ key: String) -> String { NSLocalizedString(key, comment: "") }

enum ReaderPairUi: Equatable {
    case idle
    case scanning
    case waiting(code: String)
    case failed(String)   // needs_tls | refused | not_found | denied | expired
}

final class ReaderModel: ObservableObject {
    enum Chip: Equatable { case ready, notSetUp, offline, needsManager, busy, paused }

    @Published var screen: ReaderScreen = .connecting
    @Published var enrolled = false
    @Published var mainName = ""
    @Published var pair: ReaderPairUi = .idle
    @Published var mains: [FoundMain] = []
    @Published var sheetOpen = false
    @Published var toast: String?
    @Published var requestFrom = ""      // the main till's name, for the overlay
    private var toastToken = 0

    var chip: Chip {
        guard enrolled else { return .notSetUp }
        switch screen {
        case .revoked: return .notSetUp
        case .needsSetup: return .needsManager
        case .connecting, .offline: return .offline
        case .paused: return .paused
        case .pay, .result: return .busy
        case .idle: return .ready
        }
    }

    var chipText: String {
        switch chip {
        case .ready: return rs("reader_chip_ready")
        case .notSetUp: return rs("reader_chip_not_set_up")
        case .offline: return rs("reader_chip_offline")
        case .needsManager: return rs("reader_chip_needs_manager")
        case .busy: return rs("reader_chip_busy")
        case .paused: return rs("reader_chip_paused")
        }
    }

    var chipIcon: String {
        switch chip {
        case .ready: return "checkmark.circle.fill"
        case .notSetUp: return "creditcard.trianglebadge.exclamationmark"
        case .offline: return "wifi.slash"
        case .needsManager: return "person.badge.key.fill"
        case .busy: return "creditcard.fill"
        case .paused: return "pause.circle.fill"
        }
    }

    /// The overlay is the only full-screen thing: only while a payment is genuinely waiting for a card (or its result).
    var overlayVisible: Bool {
        switch screen {
        case .pay, .result: return true
        default: return false
        }
    }

    func showToast(_ text: String) {
        toast = text
        toastToken += 1
        let t = toastToken
        UIAccessibility.post(notification: .announcement, argument: text)
        DispatchQueue.main.asyncAfter(deadline: .now() + 5) { [weak self] in
            guard let self, self.toastToken == t else { return }
            self.toast = nil
        }
    }
}

/// Forwards the session's UI callbacks (session queue) to the model (main thread).
final class ReaderModelUi: ReaderUi {
    private let model: ReaderModel
    init(model: ReaderModel) { self.model = model }

    func show(_ s: ReaderScreen) {
        DispatchQueue.main.async {
            self.model.screen = s
            // Idle-ready keeps the screen awake: a locked phone has no link (no push wake exists).
            // Also awake while a payment is requested: auto-lock would drop the link mid-sale.
            if case .pay = s { UIApplication.shared.isIdleTimerDisabled = true }
            else { UIApplication.shared.isIdleTimerDisabled = (s == .idle) }
            if case .pay = s { self.model.requestFrom = self.model.mainName }
        }
    }

    func incoming() {
        DispatchQueue.main.async {
            UINotificationFeedbackGenerator().notificationOccurred(.warning)
            UIAccessibility.post(notification: .announcement, argument: rs("reader_a11y_incoming"))
        }
    }

    func revoked() {
        DispatchQueue.main.async { self.model.enrolled = false }
        ReaderController.shared?.credentialRevoked()
    }

    func refusedWhileBusy() {
        DispatchQueue.main.async {
            let name = self.model.mainName
            self.model.showToast(name.isEmpty ? rs("reader_toast_refused_unnamed")
                                              : String(format: rs("reader_toast_refused"), name))
        }
    }
}
