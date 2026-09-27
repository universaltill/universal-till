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
            // No actions: iOS supplies its own localised OK button.
            .alert(Text("external_link_failed"), isPresented: $linkFailed) {}
        }
    }
}
