// The reader's SwiftUI surfaces (ux-guidelines: one thumb, nothing traps the
// cashier, the phone is also a till). Native views over the WKWebView till:
//  - a status chip (text AND icon, never colour alone, >= 48 pt) that opens the sheet;
//  - the reader sheet (pair / waiting code / denied / revoked / ready);
//  - the request overlay, the only full-screen thing, shown only while a payment
//    is genuinely waiting for a card; the basket underneath is never touched;
//  - the result card and the "refused while busy" toast.
// No fixed-width buttons, labels wrap (lineLimit nil), leading/trailing only.
import SwiftUI
import UIKit

struct ReaderChip: View {
    @ObservedObject var model: ReaderModel

    var body: some View {
        Button { model.sheetOpen = true } label: {
            HStack(spacing: 8) {
                Image(systemName: model.chipIcon).accessibilityHidden(true)
                Text(model.chipText).font(.footnote).lineLimit(nil).multilineTextAlignment(.leading)
            }
            .padding(.horizontal, 14)
            .frame(minWidth: 48, minHeight: 48)
            .background(.regularMaterial, in: Capsule())
        }
        .accessibilityLabel(Text(model.chipText))
        .accessibilityHint(Text(rs("reader_sheet_title")))
    }
}

/// The chip, toast, overlay and sheet in one modifier for ContentView.
struct ReaderLayer: ViewModifier {
    @ObservedObject var model: ReaderModel
    let controller: ReaderController

    func body(content: Content) -> some View {
        content
            .overlay(alignment: .top) {
                if let toast = model.toast {
                    Text(toast)
                        .font(.footnote)
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(.regularMaterial)
                        .accessibilityAddTraits(.updatesFrequently)
                        .allowsHitTesting(false)
                }
            }
            .overlay(alignment: .bottomTrailing) {
                if !model.overlayVisible {
                    ReaderChip(model: model).padding(12)
                }
            }
            .overlay {
                if model.overlayVisible { ReaderRequestOverlay(model: model, controller: controller) }
            }
            .sheet(isPresented: $model.sheetOpen) { ReaderSheet(model: model, controller: controller) }
    }
}

struct ReaderRequestOverlay: View {
    @ObservedObject var model: ReaderModel
    let controller: ReaderController
    @State private var startedAt = Date()

    var body: some View {
        ZStack {
            Color(.systemBackground).opacity(0.97).ignoresSafeArea()
            switch model.screen {
            case .pay(let minor, let currency, let timeoutMs):
                VStack(spacing: 20) {
                    Spacer()
                    if !model.requestFrom.isEmpty {
                        Text(String(format: rs("reader_request_from"), model.requestFrom)).font(.headline)
                    }
                    Text(ReaderAmount.format(amountMinor: minor, currency: currency))
                        .font(.system(size: 52, weight: .bold)).minimumScaleFactor(0.5).lineLimit(1)
                    Text(rs("reader_hold_card")).multilineTextAlignment(.center)
                    TimelineView(.periodic(from: startedAt, by: 1)) { ctx in
                        let left = Int(Double(timeoutMs) / 1000 - ctx.date.timeIntervalSince(startedAt))
                        Text(String(format: rs("reader_time_left"), ReaderAmount.clock(seconds: left))).font(.title3.monospacedDigit())
                    }
                    Spacer()
                    Button { controller.cancelLive() } label: {
                        Text(rs("reader_cancel")).frame(maxWidth: .infinity, minHeight: 48)
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.large)
                }
                .padding(24)
                .onAppear { startedAt = Date() }
                .accessibilityElement(children: .contain)
            case .result(let kind):
                VStack(spacing: 16) {
                    Image(systemName: kind == .done ? "checkmark.circle.fill" : "info.circle.fill").font(.system(size: 64)).accessibilityHidden(true)
                    Text(resultText(kind)).font(.title2).multilineTextAlignment(.center).lineLimit(nil)
                }
                .padding(24)
                .accessibilityElement(children: .combine)
            default:
                EmptyView()
            }
        }
    }

    private func resultText(_ k: ReaderResultKind) -> String {
        switch k {
        case .done: return rs("reader_result_done")
        case .cancelled: return rs("reader_result_cancelled")
        case .cardRefused: return rs("reader_result_card_not_accepted")
        case .expired: return rs("reader_result_time_up")
        case .pending: return rs("reader_result_pending")
        case .linkLost: return rs("reader_result_lost")
        }
    }
}

struct ReaderSheet: View {
    @ObservedObject var model: ReaderModel
    let controller: ReaderController
    @State private var address = ""
    @State private var tillId = ""
    @State private var manual = false
    @State private var confirmForget = false

    var body: some View {
        NavigationView {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) { content }.padding(20)
            }
            .navigationTitle(Text(rs("reader_sheet_title")))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button(rs("reader_sheet_done")) { model.sheetOpen = false } } }
        }
        .onAppear { if !model.enrolled { controller.scan() } }
    }

    @ViewBuilder private var content: some View {
        if model.enrolled && model.screen != .revoked {
            enrolledContent
        } else {
            pairContent
        }
    }

    @ViewBuilder private var enrolledContent: some View {
        switch model.screen {
        case .needsSetup:
            Text(rs("reader_needs_setup"))
            bigButton(rs("reader_set_up")) { controller.setup() }
        case .offline, .connecting:
            Text(rs("reader_offline"))
        default:
            Text(model.mainName.isEmpty ? rs("reader_chip_ready") : String(format: rs("reader_ready_for"), model.mainName)).font(.headline)
            Text(rs("reader_ready_keep_open"))
        }
        Button(role: .destructive) { confirmForget = true } label: {
            Text(rs("reader_forget")).frame(maxWidth: .infinity, minHeight: 48)
        }
        .confirmationDialog(rs("reader_forget_confirm"), isPresented: $confirmForget, titleVisibility: .visible) {
            Button(rs("reader_forget"), role: .destructive) { controller.forget() }
        }
    }

    @ViewBuilder private var pairContent: some View {
        if model.screen == .revoked {
            Text(model.mainName.isEmpty ? rs("reader_revoked_generic") : String(format: rs("reader_revoked"), model.mainName))
        }
        switch model.pair {
        case .waiting(let code):
            Text(rs("reader_pair_waiting_title")).font(.headline)
            Text(code).font(.system(size: 44, weight: .bold, design: .monospaced)).accessibilityLabel(Text(code.map(String.init).joined(separator: " ")))
            Text(rs("reader_pair_waiting_help"))
            bigButton(rs("reader_pair_cancel")) { controller.cancelPairing() }
        case .failed(let reason):
            Text(failText(reason)).font(.headline)
            bigButton(rs("reader_try_again")) { controller.cancelPairing(); controller.scan() }
        default:
            Text(rs("reader_pair_title")).font(.headline)
            if model.mains.isEmpty && !manual {
                Text(rs("reader_pair_none"))
            }
            ForEach(model.mains) { m in
                bigButton(m.shop.isEmpty ? m.address : m.shop) { controller.pair(address: m.address, tillId: m.tillId, mainName: m.shop) }
            }
            if manual {
                TextField(rs("reader_pair_address"), text: $address)
                    .textFieldStyle(.roundedBorder).keyboardType(.numbersAndPunctuation).autocorrectionDisabled().textInputAutocapitalization(.never)
                TextField(rs("reader_pair_till_id"), text: $tillId)
                    .textFieldStyle(.roundedBorder).autocorrectionDisabled().textInputAutocapitalization(.never)
                bigButton(rs("reader_pair_start")) { controller.pair(address: address, tillId: tillId, mainName: "") }
            } else {
                Button { manual = true } label: { Text(rs("reader_pair_enter_address")).frame(maxWidth: .infinity, minHeight: 48) }
            }
        }
    }

    private func failText(_ r: String) -> String {
        switch r {
        case "denied": return rs("reader_pair_denied")
        case "expired": return rs("reader_pair_expired")
        case "needs_tls": return rs("reader_pair_needs_tls")
        case "not_found": return rs("reader_pair_not_found")
        default: return rs("reader_pair_refused")
        }
    }

    private func bigButton(_ title: String, _ action: @escaping () -> Void) -> some View {
        Button(action: action) { Text(title).lineLimit(nil).frame(maxWidth: .infinity, minHeight: 48) }
            .buttonStyle(.borderedProminent)
    }
}
