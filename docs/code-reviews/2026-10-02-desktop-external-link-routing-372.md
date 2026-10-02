# 2026-10-02 — Linux desktop shell: external-link routing (ut-docs#372)

## What shipped

The Linux desktop shell (`cmd/unitill-desktop/`, WebKitGTK via the vendored
`webview_go`) had no navigation/new-window interception at all — a link
leaving the till (plain, `target="_blank"`, or `window.open()`) fell into
WebKit's addressless implicit popup instead of opening the system's default
browser. macOS (`webkit_darwin.go`) already did this correctly.

New files:
- `cmd/unitill-desktop/webkit_navigation.go` — pure policy (`isExternalNav`,
  `navigationDisposition`), untagged, unit-tested.
- `cmd/unitill-desktop/webkit_navigation_test.go` — table tests.

Extended (not new files — the Architect design merged this into the existing
ut-docs#2991 recovery signal-connect lifetime rather than a second widget
lookup):
- `cmd/unitill-desktop/webkit_recovery_cgo_linux.go` — WebKitGTK's
  `"decide-policy"` signal (plain/`target="_blank"` link clicks) and
  `"create"` signal (`window.open()`) both connected/disconnected alongside
  the existing crash-recovery signals.
- `cmd/unitill-desktop/webkit_recovery_linux.go` — `utNavDecidePolicy` and
  `utNavCreate` exported callbacks, sharing a `navRoute` helper: external →
  `g_app_info_launch_default_for_uri` (system browser); till-origin
  new-window request → load in the existing view (no second window exists
  to put it in).
- `cmd/unitill-desktop/webview_fallback.go` — comments only, documenting the
  shared teardown ordering.

Windows was explicitly split out to ut-docs#3439 (needs a new vendor-fork
COM patch + hand-written interop with zero precedent in this codebase —
different risk profile, not attempted this cycle). macOS untouched.

## Findings (independent Fable review, different model from the Opus author)

- **F1 (real, fixed before merge):** `window.open()` never reaches
  `decide-policy` at all — WebKitGTK fires the `"create"` signal instead.
  The first implementation only handled `decide-policy`
  (NAVIGATION_ACTION/NEW_WINDOW_ACTION), so a `target="_blank"` **link
  click** was correctly routed but a JS `window.open()` call silently did
  nothing (confirmed live under Xvfb, both before and after the fix — see
  below). Fixed by connecting `"create"` in the same signal lifetime and
  routing through the same `navigationDisposition`/`navRoute`. No current
  call site in this codebase uses `window.open()` (grep: only comments
  saying "never target=_blank/window.open"), so this wasn't a live
  regression, but it was in-scope per the card's own title (new-window
  requests) and is now closed.
- **F2 (verified, accepted):** WebKitGTK's UI-process API exposes no
  main-frame flag on a navigation action (confirmed against the real
  installed 2.52.6 headers, not just the code's own comment), so
  `decide-policy` routing also applies to subframe navigations. The till's
  only iframe is the sandboxed `srcdoc` plugin-page embed
  (`plugin_page.go`'s `pluginPageCSP`: `frame-src 'self' about:`), so a
  nested cross-origin iframe is already blocked by CSP before this signal
  would even matter, and no legitimate same-origin-but-off-origin subframe
  navigation exists in the codebase today. Accepted as-is; worth revisiting
  only if a future card adds a different kind of iframe.
- **F3 (pre-existing, #2991, out of scope):** on a window-manager close
  (not the normal `w.Destroy()` path), the vendored `webview.h` nulls its
  internal view pointer before this shell's own teardown runs, so
  `recoveryDisconnect`'s `WEBKIT_IS_WEB_VIEW`/`g_signal_handlers_disconnect_by_func`
  calls can run against already-finalized GTK state. This diff's new 4th/5th
  disconnect calls inherit the exact same pre-existing exposure as the
  original three (#2991) — not introduced here, not reproduced (needs a
  real WM close under Xvfb, not exercised by either card's tests).
  Suggested follow-up for whoever picks it up: `g_object_add_weak_pointer`
  on the view, or hook the view's own `"destroy"` to clear the Go-side
  pointer first. Not filed as a separate card — low severity, shutdown-time
  only, same owner likely to encounter it as #2991's own maintainer.

## What was verified beyond automated tests

- **Dev's own real driven run** (temporary test, deleted, not in the diff):
  a real `webview.New` + the real signal-connect path under Xvfb, with a
  fake XDG default-browser handler, confirmed a `target="_blank"` link
  click and a plain external link both reached the fake browser with the
  correct URL, and a same-origin `target="_blank"` loaded in the existing
  view.
- **Tester's independent re-verification** (separately written, not reusing
  Dev's test file): same real-Xvfb approach, confirmed the external link
  case and that the till's own server never received a second navigation.
- **Independent Fable review's real driven run** (separately written again):
  exercised plain/target=_blank/window.open/location.href, for both
  external and same-origin targets, logging the view's actual URI after
  each step. This is what surfaced F1 — `window.open()` produced no
  `decide-policy` call at all (confirmed via a temporary `"create"`-signal
  probe) while every other case worked.
- **This fix's own re-verification** (after F1's code change): a fourth,
  independently-written real-Xvfb test confirmed `window.open()` now
  reaches the fake default browser with the correct URL.
- **TDD re-verification**: `isExternalNav`/`navigationDisposition` bodies
  were stubbed to a trivial/wrong implementation; `webkit_navigation_test.go`
  failed with real, specific assertion errors (13 failures, not a build
  error); restored and re-passed. Done independently by both the Reviewer
  and (implicitly, via Dev's own TDD-first process) the author.
- `gofmt`, `go build -tags desktop ./...` (whole repo), `go vet` (tagged and
  untagged), `go test` (tagged and untagged, package-scoped), full `-v`
  run of all 162+ tests in the package under Xvfb (only the expected
  no-display skip), `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-deadcode-baseline.sh` — all clean, each run independently by Dev,
  Tester, and the Reviewer.
- **Not verified:** no real shop hardware/kiosk device (not available in
  this environment) — the till's production desktop shell has not been run
  on real Linux hardware for this change, only under Xvfb in a container.
  No visual/UI surface changed, so no screenshot review applies (confirmed
  by Architect's "skip UX" call — this is shell chrome, not a rendered
  page).

## Pre-existing issue found, filed separately (not fixed here)

`TestDesktopWindowOps_ExitToOSRecordsAppliedMode` intermittently SIGABRTs
under Xvfb — reproduced identically on a clean `main` with this diff
stashed out, so unrelated to this change. Filed as ut-docs#3445.

## Safe to merge

Yes. F1 fixed and re-verified; F2 accepted with reasoning recorded; F3 is
pre-existing and out of scope. All gates green, independently run three
times by three different passes (Dev, Tester, Reviewer) plus a fourth after
the F1 fix.
