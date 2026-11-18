// The slim, non-modal Tap to Pay status banner (ut-docs#3049, design §4.2,
// §6): shown only while the reader is being prepared ("Getting Tap to Pay
// ready… 40 %", determinate when progress is known, per Apple's HIG) or
// right after a failed setup. It never blocks a touch on the till, and the
// failed-setup line goes away by itself: it lies over the bottom edge of
// the sell screen, so it must not stay there until the next setup.
import SwiftUI
import UIKit

final class TapToPayStatusModel: ObservableObject {
    /// How long the failed-setup line stays up.
    static let failureShownFor: TimeInterval = 8

    @Published private(set) var capability: Capability?
    @Published private(set) var setupFailed = false
    private var failureShown = false
    private var failureToken = 0

    /// Main thread only.
    func update(_ cap: Capability, setupFailed failed: Bool) {
        capability = cap
        if !failed {
            failureShown = false
            setupFailed = false
            return
        }
        // The coordinator repeats "failed" on every later capability
        // change; show (and announce) each failure once.
        guard !failureShown else { return }
        failureShown = true
        setupFailed = true
        // The banner is not focusable and VoiceOver does not read a view
        // that merely appears, so say it.
        UIAccessibility.post(notification: .announcement, argument: Self.setupFailedMessage)
        failureToken += 1
        let token = failureToken
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.failureShownFor) { [weak self] in
            guard let self, self.failureToken == token else { return }
            self.setupFailed = false
        }
    }

    var preparing: Bool { capability?.state == .preparing }
    var visible: Bool { preparing || setupFailed }

    static var setupFailedMessage: String {
        NSLocalizedString("ttp_setup_failed",
                          value: "Tap to Pay on iPhone couldn't be set up. Open Settings › Payments to try again.",
                          comment: "Tap to Pay banner after a failed setup")
    }
}

struct TapToPayStatusBanner: View {
    @ObservedObject var model: TapToPayStatusModel

    var body: some View {
        if model.visible {
            HStack(spacing: 12) {
                if model.preparing {
                    if let p = model.capability?.progress, p > 0 {
                        ProgressView(value: Double(p), total: 100)
                            .frame(maxWidth: 80)
                    } else {
                        ProgressView()
                    }
                }
                Text(message)
                    .font(.footnote)
                    .multilineTextAlignment(.leading)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: 44)
            .background(.regularMaterial)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text(message))
            .accessibilityAddTraits(.updatesFrequently)
            .allowsHitTesting(false)
        }
    }

    private var message: String {
        if model.preparing {
            if let p = model.capability?.progress, p > 0 {
                return String(format: NSLocalizedString("ttp_preparing_progress",
                                                        value: "Getting Tap to Pay ready… %d%%",
                                                        comment: "Tap to Pay banner with percent"), p)
            }
            return NSLocalizedString("ttp_preparing", value: "Getting Tap to Pay ready…",
                                     comment: "Tap to Pay banner while preparing")
        }
        return TapToPayStatusModel.setupFailedMessage
    }
}
