import UIKit
import WebKit

/// Native screenshot for the bug-report panel (ut-docs#3355). A WKWebView
/// has nothing for getDisplayMedia to record, so the web capture path only
/// ever got the system share prompt. The panel calls
/// `window.webkit.messageHandlers.utScreenshot.postMessage('capture')`
/// instead; the promise resolves to the till page as a
/// `data:image/png;base64,…` URL, or `""` on any failure — the same contract
/// as Android's `AndroidKiosk.captureScreenshot()` (ut-docs#1435).
///
/// Only the till's own main frame may ask: a sub-frame or any other origin
/// gets `""`. "The till's origin" is the web view's current page, not a
/// URL captured at setup: an in-app restart can move the server to a new
/// port and reload the same web view there, and the navigation policy
/// already confines that page to the till origin. The snapshot is of this
/// web view only, never the rest of the screen or another app. The web view is held weakly — the user content
/// controller retains this handler, and the web view retains the controller.
final class ScreenshotBridge: NSObject, WKScriptMessageHandlerWithReply {
    static let name = "utScreenshot"

    weak var webView: WKWebView?

    func userContentController(_ userContentController: WKUserContentController,
                               didReceive message: WKScriptMessage,
                               replyHandler: @escaping (Any?, String?) -> Void) {
        let origin = message.frameInfo.securityOrigin
        guard message.frameInfo.isMainFrame,
              let webView = webView,
              let tillOrigin = webView.url,
              origin.protocol == tillOrigin.scheme,
              origin.host == tillOrigin.host,
              origin.port == (tillOrigin.port ?? 0),
              webView.bounds.width > 0, webView.bounds.height > 0 else {
            replyHandler("", nil)
            return
        }
        let config = WKSnapshotConfiguration()
        // Wait for pending paints: the panel hides itself just before asking.
        config.afterScreenUpdates = true
        webView.takeSnapshot(with: config) { image, _ in
            guard let image = image else {
                replyHandler("", nil)
                return
            }
            // A full-resolution PNG takes a while to encode: keep it off the
            // main thread, reply back on it.
            DispatchQueue.global(qos: .userInitiated).async {
                let dataURL = image.pngData().map { "data:image/png;base64," + $0.base64EncodedString() } ?? ""
                DispatchQueue.main.async { replyHandler(dataURL, nil) }
            }
        }
    }
}
