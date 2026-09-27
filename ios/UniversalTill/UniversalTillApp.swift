// The iOS/iPadOS till shell (ut-docs#3068, ADR-0023 §1): the same Go till
// server every other platform runs, started in-process through the
// gomobile-bound ../mobile package, shown in a WKWebView. The server owns
// the whole product; this app is a thin, disposable window around it —
// the iOS counterpart of android/app/.../MainActivity.kt.
import SwiftUI

@main
struct UniversalTillApp: App {
    @StateObject private var till = TillController()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            ContentView(till: till)
                .onAppear { till.boot() }
        }
        .onChange(of: scenePhase) { phase in
            if phase == .active {
                till.resume()
            }
        }
    }
}
