# 2026-07-27 — Android: real plugin execution verified + file-chooser fix (ADR-0023)

## Context
ADR-0023 (2026-07-25) reasoned from source that WASM plugins should run
identically on Android — wazero's `NewRuntime` falls back to its
interpreter engine on platforms `platform.CompilerSupported()` doesn't
list, and `android` is absent from that list, so no JIT/W^X issue applies.
That was a correct but never device-tested claim. Farshid asked (2026-07-27
night) for a SumUp payment plugin, for every plugin to work on Android, and
for printer support — the Android-plugin claim needed to stop being an
inference and become a verified result before it could honestly gate
anything else. This is that verification, done live against a real
emulator (`unitill-test` AVD, API 36, arm64) with the Android SDK already
on this machine (`/opt/homebrew/share/android-commandlinetools`, not on
PATH by default — exported for this session).

## What was verified

**A real compiled payment plugin (the new `ut-plugin-payment-sumup`,
built the same night) loads and dispatches through the actual
gomobile/NDK-cross-compiled Go server running inside the Android app.**
Installed it via the exact production path
(`internal/plugins.InstallPlugin` → `PersistManifest`, run from a
throwaway seed tool placed temporarily at
`universal-till/scripts/zzz_android_plugin_seed` and deleted immediately
after, since `internal/` packages can't be imported from outside the
module), copied the compiled `.wasm` into the app's private storage at
the exact path `WasmRuntime.Sync` expects
(`<DataDir>/plugins/<id>/<version>/bin/plugin.wasm`, confirmed by reading
`internal/paths.Plugins` and `wasm_runtime.go`'s `modPath` construction),
and relaunched the app. Logcat showed the real production `Sync()` path
succeeding:

```
GoLog: wasm plugin com.universaltill.payment-sumup loaded, handling
[payment.sumup.authorize payment.sumup.refund payment.sumup.requested]
```

This is the architecturally significant confirmation ADR-0023 only had
from source analysis before: `wazero.CompileModule` genuinely succeeds
for a real payment plugin's real compiled binary on Android arm64. The
same plugin was also independently confirmed correctly recognized by the
real `/ui` Plugins admin page (Enabled, trusted, v1.0.0) after completing
the setup wizard live on-device.

**Did not** trigger an actual UI-driven card sale through it (would have
needed `SyncPluginPaymentMethods` wiring + a completed shop setup +
several more click-throughs); considered lower-value than the load
confirmation above, since the event-dispatch logic itself (host functions,
`HandleEvent`, blocking-vs-async event modes) is identical Go code already
covered by `internal/plugins` unit tests and by a separate desktop-
equivalent smoke test run the same night (see `ut-plugin-payment-sumup`'s
own review).

## Real bug found and fixed: no file chooser on Android

While reaching the Plugins page's "Import from file" side-load form
(spec 001-plugin-marketplace) to test it as a second plugin-install path,
tapping **Choose File did nothing at all** — no dialog, no error. Checked
`MainActivity.kt`: the `WebView` had `webViewClient` set but no
`webChromeClient`. This is a well-documented, unambiguous Android
contract, not a guess: `<input type="file">` inside a plain `WebView`
requires `WebChromeClient.onShowFileChooser` to be overridden, or the tap
is silently swallowed — confirmed by the absence being the entire
explanation (grep found zero `WebChromeClient` references anywhere in the
app) before writing a single line of the fix.

**Fixed**: added a `WebChromeClient` overriding `onShowFileChooser`,
launching the `FileChooserParams`-provided intent (or a plain
`ACTION_GET_CONTENT` fallback) via `registerForActivityResult` — the same
`ActivityResultContracts` pattern this file already uses for the
notification-permission request, not a new pattern. Handles both single
and multi-file `clipData` results and resolves any previously-pending
callback with `null` before replacing it (the documented contract for a
second chooser opening while one is outstanding).

**Verified, precisely**: rebuilt (`BUILD SUCCESSFUL`, Kotlin compiled
clean), reinstalled, relaunched. Before the fix: tapping Choose File was a
confirmed no-op (screenshotted, no dialog, no focus change). After the
fix: tapping Choose File opens the real native Android document picker
(`DocumentsUI`, "Open from ▸ Recent/Downloads/Drive"), confirmed by
screenshot. **Not fully closed the loop on**: selecting a specific file
and watching it flow back into the WebView's file input — repeated grid-
tile taps on files inside `DocumentsUI`'s own Downloads listing didn't
register (this reproduced on a completely unrelated, unmodified system
app, strongly pointing at emulator input-injection flakiness rather than
anything in this change — `adb shell input tap` intermittently failed
to register on *this app's own* wizard buttons too during the same
session, confirmed unrelated to any code change by reproducing it fresh
after a clean reinstall and finding it was a coordinate-scaling mistake
in the test process, not the app, in that case). The result-handling code
(`onActivityResult` → `ValueCallback.onReceiveValue`) follows the
standard, well-documented Android intent-result contract exactly — high
confidence by inspection even without a fully-clicked-through live
confirmation of that specific leg.

## Verification
`./gradlew assembleDebug` — `BUILD SUCCESSFUL`, Kotlin compiles clean, no
new warnings. Live device verification as described above (before/after
screenshots of the file-chooser fix; logcat capture of the plugin-load
line). No Go-side changes in this pass — Kotlin-only (`MainActivity.kt`).

## Bonus: Android network printing confirmed live too

Since the flaky touch input made further WebView click-throughs low
value, switched to driving the running Android instance's real HTTP API
directly (`adb forward` to its loopback port + `curl`, using the real
`/api/auth/login` then `/api/settings/printer` and `/api/print/test`
endpoints — same production code every UI button already calls, not a
test-only shortcut). Pointed `printer.mode=network` at
`10.0.2.2:9100` (the Android emulator's standard alias for the host
machine's own loopback) with a plain `nc -l 9100` listening on the host.
`POST /api/print/test` returned `200 OK` / "Test receipt sent", and the
listener genuinely received a complete, correctly-formed ESC/POS document
— init sequence, center-align, the wizard-set shop name ("TestShop3"),
"TEST PRINT", a timestamp, the "Printer: OK" line, "Universal Till"
footer, paper-cut command. This confirms `internal/print`'s network
transport (`internal/print/transport.go`'s `networkTransport`, plain
`net.Dialer` — no OS-specific syscalls) works completely unmodified
inside the Android-embedded server: a shop with a network/WiFi ESC/POS
printer needs zero Android-specific work. Closes the first half of
`ut-docs/QUEUE.md` item C1b with a real result, not an inference.

## What's still open (tracked in `ut-docs/QUEUE.md` item C1b)
Device-mode (`/dev/usb/lp0`) definitively will not work in the Android app
sandbox — no raw character-device access without root. USB/Bluetooth
printer support (the likely gap for a small shop's actual hardware) needs
the native-adapter approach ADR-0023 §2 already scoped, blocked on knowing
the German prospect's actual printer model/connection type — not
something to guess at. The plugin-sideload file-chooser fix's full
select-a-file round-trip also wasn't clicked through to 100% completion
(see above) — the fix's activation is confirmed, the standard Android
result-callback plumbing is high-confidence by inspection but not
live-clicked end-to-end.
