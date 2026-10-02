# Review: iOS bug-report screenshot bridge (ut-docs#3355)

- **Date:** 2026-10-02
- **Branch:** `fix/3355-ios-screenshot-bridge`
- **Author:** pipeline lane:cloud-54 (Opus 5.5)
- **Reviewer:** independent Fable subagent, one round (card is `complexity:medium`)

## What shipped

In the iOS app, "Take screenshot" in the bug-report panel captured only the
iOS share prompt: a WKWebView has nothing for `getDisplayMedia` to record.

- `ios/UniversalTill/ScreenshotBridge.swift` (new): a
  `WKScriptMessageHandlerWithReply` named `utScreenshot`, registered in the
  page content world by `TillWebView.swift`. It answers only the main frame
  whose origin matches the web view's current (till) page, runs
  `WKWebView.takeSnapshot` (after screen updates), encodes the PNG off the
  main thread, and replies with a `data:image/png;base64,…` URL, or `""` on
  any failure. This is the same contract as Android's
  `AndroidKiosk.captureScreenshot()`. The bridge holds the web view weakly.
- `web/ui/partials/bugreport_panel.html`: a new branch, after Android and
  before `getDisplayMedia`, calls the handler. The panel hides itself
  (`visibility: hidden`) for two frames so that on a phone the shot shows
  the till rather than the panel, and is restored on every path
  (success, `""`, rejection, synchronous throw). The data-URL→thumbnail
  step is now one helper shared with the Android branch, with no change in
  behaviour.
- Tests: `e2e/tests/ios-screenshot-bridge-3355.spec.ts` (stubbed handler:
  thumbnail, panel hidden during capture and restored, `""`, rejection,
  and a host with `window.webkit` but no `utScreenshot` still uses
  `getDisplayMedia`). There is also a wiring guard,
  `scripts/ci/ios-screenshot-bridge_test.sh`, with broken-fixture
  self-tests, wired into `ci.yml`.
- Docs: help topic `bug-reporting` (en/de/tr; fa/ar have no native-app
  paragraph, a pre-existing gap) and `ios/README.md`. The docs-shots
  manifest was regenerated; no PNG changed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The bridge compared against a till origin captured once in `makeUIView`. An in-app server restart on a new port reloads the same web view at the new origin, after which every screenshot would fail. | **Fixed.** The bridge now compares against `webView.url`, which the navigation policy confines to the till origin. The guard pins it with a self-test fixture. |
| 2 | minor | Spec test 4 did not prove its claim: it passed whether or not the iOS branch was taken. | **Fixed.** It now stubs `getDisplayMedia`, clicks, and asserts that the call happened. |
| 3 | nit | Snapshots are taken at full device resolution (several MB on an iPad Pro). | Accepted: same as Android, and within the 32 MB report cap. |
| 4 | nit | A guard regex matched commented-out code and contained an unescaped `.`. | **Fixed.** It now checks code lines only and escapes the `.`, with a new self-test fixture. |
| 5 | nit | The Android comment now sits above the shared helper. | Accepted: the helper has its own one-line heading. |

## Verified

- TDD: the reviewer reverted `bugreport_panel.html` and confirmed that the iOS
  spec's tests 1–3 fail, then restored it and confirmed they pass.
- e2e: the iOS, Android bridge and `bugreport-panel` specs pass (25 tests).
- `go build ./...`, `go test` for pages/httpx/web, and every guard in
  `ci.yml`'s build job pass locally. The only local failure was
  `guard-shellcheck-version`, because the container has shellcheck 0.11 and
  CI pins 0.9; the new script is clean under 0.11.
- The Swift was not compiled here because the container has no Xcode. The
  reviewer read it against the iOS 15 / Xcode 26 APIs, and `ios-ci` compiles
  it and runs the simulator smoke test on this PR.
- Not device-checked: no iPhone was reachable from this cloud lane. The
  real-device check is the owner's TestFlight build after release.

## Verdict

Safe to merge once `ios-ci` (the Swift compile) and `ci` are green.

## After push

- CI `build` failed on `guard-pipefail-grep-q` (ut-docs#2946). The new guard
  piped `grep -v` into `grep -q` under `pipefail`. Fixed by using a
  here-string. I re-ran every build-job guard locally and all pass.

## Rebase onto main (2026-10-02, sweep)

The PR went `CONFLICTING` after ut-docs#3354 merged. That change touched the
same help topic and added a sentence saying screenshots are not available
in the iPhone and iPad app. The help text merged cleanly but contradicted
this PR; only the docs-shots manifest showed a conflict.

- Resolution: in all five locales, the iOS paragraph now says that "Take
  screenshot" captures the till screen itself and that only screen recording
  is unavailable. The manifest was regenerated with
  `e2e/tests-docs/write-manifest.js`.
- Independent review of the resolution, on a different model (Fable):
  no blockers. It raised one minor finding: fa/ar did not describe the new
  iOS behaviour. That is now **fixed**, with a sentence added in both. It
  also raised one nit: the iOS sentence sat in the Android paragraph. That
  is now **fixed**, and the sentence is in the iOS paragraph.
- Re-run on the rebased head: `guard-docs-shots`, `guard-help-drift`,
  `guard-help-topics`, `guard-i18n`, `guard-compliance-claims`,
  `guard-competitor-naming`, `guard-no-showmodal`, `guard-pipefail-grep-q`,
  `guard-ios-usage-descriptions`, `ios-screenshot-bridge_test` and
  `go build ./...` all pass. The e2e specs `ios-screenshot-bridge-3355`,
  `bugreport-voice-mime-3354`, `android-screenshot-bridge-1435` and
  `bugreport-panel` pass (27 tests).
