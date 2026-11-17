// Apple's "How to Tap" merchant education (ut-docs#3049), iOS 18+ — the
// capability check already requires iOS 18, so there is no fallback.
// https://docs.stripe.com/terminal/payments/setup-reader/tap-to-pay?platform=ios#merchant-education
import Foundation
import ProximityReader
import UIKit

final class ProximityReaderEducation: EducationPresenter {
    func presentHowToTap(_ done: @escaping (String?) -> Void) {
        guard #available(iOS 18.0, *) else {
            done(TapToPayReason.iosTooOld)
            return
        }
        Task { @MainActor in
            guard let top = TopViewController.find() else {
                done(TapToPayReason.other)
                return
            }
            do {
                let discovery = ProximityReaderDiscovery()
                let content = try await discovery.content(for: .payment(.howToTap))
                try await discovery.presentContent(content, from: top)
                done(nil)
            } catch {
                done(TapToPayReason.other)
            }
        }
    }
}

/// The view controller to present from: the key window's root, walked down
/// presentedViewController.
enum TopViewController {
    @MainActor
    static func find() -> UIViewController? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let window = scenes.flatMap(\.windows).first { $0.isKeyWindow } ?? scenes.first?.windows.first
        var top = window?.rootViewController
        while let next = top?.presentedViewController { top = next }
        return top
    }
}
