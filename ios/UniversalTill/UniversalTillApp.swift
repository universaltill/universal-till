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
                .onAppear {
                    till.boot()
                    TapToPayRegistration.activate()
                }
        }
        .onChange(of: scenePhase) { phase in
            // Tap to Pay (ut-docs#3049, ADR-0125 §3e): registered while
            // active, cleared in the background, and deliberately left alone
            // on .inactive — Apple's own sheets cause it mid-payment. Both
            // calls are no-ops unless the build's UT_TAP_TO_PAY flag is on.
            if phase == .active {
                till.resume()
                TapToPayRegistration.activate()
            } else if phase == .background {
                TapToPayRegistration.deactivate()
            }
        }
    }
}
