import SwiftUI
import WebKit

/// The till's UI: a WKWebView on the in-process server's loopback origin.
/// Navigation is confined to that origin; any other http(s)/mailto/tel link
/// is handed to the system (Safari, Mail…), like Android's
/// openInSystemBrowser (ut-docs#1647).
struct TillWebView: UIViewRepresentable {
    let baseURL: URL
    let reloadToken: Int
    @Binding var loadFailed: Bool
    @Binding var linkFailed: Bool

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default() // keep the login cookie across launches
        config.allowsInlineMediaPlayback = true // camera barcode viewfinder
        let webView = WKWebView(frame: .zero, configuration: config)
        webView.navigationDelegate = context.coordinator
        webView.uiDelegate = context.coordinator
        webView.allowsBackForwardNavigationGestures = false
        #if DEBUG
        if #available(iOS 16.4, *) {
            webView.isInspectable = true
        }
        #endif
        context.coordinator.loadedToken = reloadToken
        webView.load(URLRequest(url: baseURL))
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        context.coordinator.parent = self
        // A (re)start of the server: load its start page again.
        if context.coordinator.loadedToken != reloadToken {
            context.coordinator.loadedToken = reloadToken
            loadFailed = false
            webView.load(URLRequest(url: baseURL))
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        var parent: TillWebView
        var loadedToken = -1

        init(_ parent: TillWebView) { self.parent = parent }

        private func isTillOrigin(_ url: URL) -> Bool {
            url.scheme == parent.baseURL.scheme &&
                url.host == parent.baseURL.host &&
                url.port == parent.baseURL.port
        }

        private func openExternally(_ url: URL) {
            guard let scheme = url.scheme?.lowercased(),
                  ["http", "https", "mailto", "tel"].contains(scheme) else { return }
            UIApplication.shared.open(url) { [weak self] ok in
                if !ok { self?.parent.linkFailed = true }
            }
        }

        func webView(_ webView: WKWebView,
                     decidePolicyFor action: WKNavigationAction,
                     decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            guard let url = action.request.url else { return decisionHandler(.cancel) }
            if isTillOrigin(url) || url.scheme == "about" || url.scheme == "blob" || url.scheme == "data" {
                return decisionHandler(.allow)
            }
            // Only a user's own tap leaves the app; a page can't bounce the
            // operator out on its own.
            if action.navigationType == .linkActivated, action.targetFrame?.isMainFrame ?? true {
                openExternally(url)
            }
            decisionHandler(.cancel)
        }

        // target="_blank": same origin stays in this view, anything else
        // goes to the system.
        func webView(_ webView: WKWebView,
                     createWebViewWith configuration: WKWebViewConfiguration,
                     for action: WKNavigationAction,
                     windowFeatures: WKWindowFeatures) -> WKWebView? {
            if let url = action.request.url {
                if isTillOrigin(url) {
                    webView.load(action.request)
                } else {
                    openExternally(url)
                }
            }
            return nil
        }

        // Camera for barcode scanning: only the till's own page may ask.
        @available(iOS 15.0, *)
        func webView(_ webView: WKWebView,
                     requestMediaCapturePermissionFor origin: WKSecurityOrigin,
                     initiatedByFrame frame: WKFrameInfo,
                     type: WKMediaCaptureType,
                     decisionHandler: @escaping (WKPermissionDecision) -> Void) {
            let same = origin.protocol == parent.baseURL.scheme &&
                origin.host == parent.baseURL.host &&
                origin.port == (parent.baseURL.port ?? 0)
            decisionHandler(same ? .grant : .deny)
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            parent.loadFailed = false
        }

        func webView(_ webView: WKWebView,
                     didFailProvisionalNavigation navigation: WKNavigation!,
                     withError error: Error) {
            mainFrameFailed(error)
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            mainFrameFailed(error)
        }

        private func mainFrameFailed(_ error: Error) {
            let e = error as NSError
            // A cancelled load (a newer navigation replaced it, or we
            // refused it above) is not a failure the operator must see.
            if e.domain == NSURLErrorDomain && e.code == NSURLErrorCancelled { return }
            if e.domain == "WebKitErrorDomain" && e.code == 102 { return } // frame load interrupted by policy
            parent.loadFailed = true
        }

        // iOS kills a backgrounded WebView's content process under memory
        // pressure; bring the page back rather than leave a blank screen.
        func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
            webView.load(URLRequest(url: parent.baseURL))
        }
    }
}
