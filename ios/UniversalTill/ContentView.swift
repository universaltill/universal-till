import SwiftUI

struct ContentView: View {
    @ObservedObject var till: TillController
    @State private var loadFailed = false
    @State private var linkFailed = false

    var body: some View {
        switch till.state {
        case .starting:
            VStack(spacing: 16) {
                ProgressView()
                Text("status_starting")
            }
        case .failed(let message):
            VStack(spacing: 16) {
                Text(String(format: NSLocalizedString("status_failed", comment: ""), message))
                    .multilineTextAlignment(.center)
                    .padding(.horizontal)
                Button("action_retry") { till.retry() }
            }
        case .running(let url):
            TillWebView(
                baseURL: url,
                reloadToken: till.reloadToken,
                loadFailed: $loadFailed,
                linkFailed: $linkFailed
            )
            .overlay(alignment: .top) {
                if loadFailed {
                    // Android's #1457 error bar: a main-frame load failed
                    // before the server's own translated error page could
                    // render, so this needs its own strings.
                    HStack {
                        Text("error_bar_message")
                        Spacer()
                        Button("error_bar_action") { till.reloadHome() }
                    }
                    .padding()
                    .background(.regularMaterial)
                }
            }
            .overlay(alignment: .bottom) {
                // Tap to Pay readiness (ut-docs#3049): non-modal, flag-gated.
                if TapToPayRegistration.enabled {
                    TapToPayStatusBanner(model: TapToPayBridge.shared.status)
                }
            }
            .modifier(ReaderLayerIfEnabled())
            // No actions: iOS supplies its own localised OK button.
            .alert(Text("external_link_failed"), isPresented: $linkFailed) {}
        }
    }
}

/// Phone card-reader mode (ut-docs#6538): present only when the build's
/// UT_READER_MODE flag is on, otherwise the till view is untouched.
private struct ReaderLayerIfEnabled: ViewModifier {
    func body(content: Content) -> some View {
        if let controller = ReaderController.shared {
            content.modifier(ReaderLayer(model: controller.model, controller: controller))
        } else {
            content
        }
    }
}
