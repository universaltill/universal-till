# Review: Android till's Online light never turned Offline (ut-docs#3088)

Branch `fix/3088-webview-network-state`. Author: Opus 5.5 (`:24` cloud lane).
Independent review: Fable (read-only subagent), one round.

## What shipped

- **Root cause.** The status bar's Online/Offline light (`#sb-conn`,
  `web/ui/layouts/base.html`) is `navigator.onLine` plus the window
  `online`/`offline` events. An Android WebView changes `navigator.onLine`
  only when the app calls `WebView.setNetworkAvailable()`. The app never
  did, and it didn't hold `ACCESS_NETWORK_STATE`, so the light stayed on
  "Online" with Wi-Fi off. Windows (WebView2) and the Pi (Chromium) track
  the network themselves; they are unchanged.
- `AndroidManifest.xml`: `ACCESS_NETWORK_STATE` (normal install-time
  permission, no prompt, no implied hardware feature).
- `MainActivity.kt`: `watchNetwork()` registers a default-network
  callback in `onCreate` and tells the WebView on the UI thread
  (`onAvailable` → online; `onLost` → offline unless another default
  network already took over), and sets the starting state explicitly so a
  till booted with no network shows Offline. `unwatchNetwork()` in
  `onDestroy` runs before `webView.destroy()`, and a posted update checks
  that the callback is still registered, so nothing touches a destroyed
  WebView.
- **Semantics: "has a network", not "validated".** Android's validation
  probes a Google endpoint, which fails in cloud-restricted markets and
  behind some shop firewalls on a network that works; the light would then
  be stuck on Offline there. Whether the cloud itself answers (Wi-Fi up, no
  internet) is a separate signal — follow-up card filed.
- Guard `scripts/ci/guard-android-network-state.sh` (+ `_test.sh`, wired
  into `ci.yml`'s build job): the permission is declared and
  `.setNetworkAvailable(` is called outside a comment.
- Manual: one sentence in the "Selling with no network connection" bullet
  of `web/help/{en,de,fa,ar,tr}/sell.md`; `web/help/img/manifest.json`
  topic hashes refreshed (`make docs-shots`). The screens are unchanged
  (surface hash identical), so the PNGs this container re-rendered —
  every one differed by font rasterisation only — were not committed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `navigator.onLine` also feeds the checkout `offline` flag (`web/public/app.js` pay body, refund, self-order), so an Android till with no network now records sales as offline (sync-queued) and skips the fiscal-sign dispatch (`fiscal_sign_hook.go`, ADR-0044 D1). | **Accepted as intended**: it is exactly what Windows and the Pi already do with the network down, and what ADR-0044 D1 specifies; before this fix Android wrongly claimed "online" for those sales. Documented in the help sentence ("recorded as offline sales, the same as with the offline checkbox") and here. |
| 2 | should-fix | No review record. | Fixed (this file). |
| 3 | nit | On a till joined to a main till the light reads **No internet**, not Offline (ut-docs#2742). | Fixed in all five help files, using each locale's `status.no_internet` wording (de from the pack's usual term "Kein Internet" — the de pack isn't checked out here). |
| 4 | nit | Guard matched a declared-but-uncalled `fun setNetworkAvailable(`, and treated `//` inside a URL as a comment. | Fixed: requires `.setNetworkAvailable(`; `//` is a comment only at line start or after whitespace. Two fixtures added. |
| 5 | nit | Test cleanup `rmdir` fails if an extra file lands in the temp dir. | Fixed: `rm -rf` of its own `mktemp -d`. |
| 6 | nit | Local function named `apply` shadows the stdlib scope function. | Renamed `tellWebView`. |

## Verified

- TDD: the guard was written first and failed on the pre-fix tree for both
  halves (missing permission, missing call); re-run after review against
  `HEAD^`'s manifest and activity — still fails, exit 1; passes on the fix.
  The reviewer independently reproduced both.
- `go build ./...`, `go test ./...` (exit 0), `gofmt`; every guard in
  `ci.yml`'s build job was run: all pass except three that fail
  identically without this change in this container (deadcode baseline,
  shellcheck version pin 0.9.0 vs the pip 0.11.0 installed here) and
  docs-shots before the manifest refresh (passes after).
- `shellcheck` on both new scripts: 0 issues.
- **Not verified here:** the Kotlin compiles only in `android-ci.yml` (no
  Android SDK in the cloud container), and nobody has turned Wi-Fi off on
  the TECLAST tablet with this build. The device check is a separate
  `blocked:env` card for the local lane.

## Verdict

Safe to merge once `android-ci` compiles the Kotlin green. No SQL, money,
i18n key, plugin-signing or file-write changes.
