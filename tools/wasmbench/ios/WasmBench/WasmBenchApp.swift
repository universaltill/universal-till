// The iOS WASM bench (ut-docs#3918): on launch, runs mobilebench over the
// bundled modules, prints the JSON report between markers on stdout (read by
// `devicectl device process launch --console`, see run.sh), saves it to
// Documents/wasmbench.json and shows it on screen.
import Mobilebench
import SwiftUI

@main
struct WasmBenchApp: App {
    var body: some Scene {
        WindowGroup { BenchView() }
    }
}

struct BenchView: View {
    @State private var report = "Running…"

    var body: some View {
        ScrollView {
            Text(report)
                .font(.system(.caption, design: .monospaced))
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding()
        }
        .task { await run() }
    }

    private func run() async {
        let dir = Bundle.main.resourceURL!.appendingPathComponent("modules").path
        let args = ProcessInfo.processInfo.environment
        let n = Int(args["WASMBENCH_N"] ?? "") ?? 30
        let calls = Int(args["WASMBENCH_CALLS"] ?? "") ?? 1000
        let json = await Task.detached(priority: .userInitiated) {
            MobilebenchRunJSON(dir, n, calls)
        }.value
        print("WASMBENCH-JSON-BEGIN\n\(json)\nWASMBENCH-JSON-END")
        fflush(stdout)
        if let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first {
            try? json.write(to: docs.appendingPathComponent("wasmbench.json"), atomically: true, encoding: .utf8)
        }
        report = json
        // run.sh sets this so `--console` returns once the report is out.
        if args["WASMBENCH_EXIT"] == "1" { exit(0) }
    }
}
