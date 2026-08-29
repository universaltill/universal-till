# Code review — Catalog export "Save CSV to Downloads" fails on Android (ut-docs#1258)

- **Date:** 2026-08-29
- **Branch:** `fix/1258-android-catalog-export-save`
- **Reviewer:** independent reviewer (fresh-context Opus subagent, this
  pipeline's `complexity:medium` review tier per the `scrum-master` skill's
  "Model routing by complexity"), isolated worktree.
- **Verdict: SAFE TO MERGE.** The reviewer's initial pass found one
  CI-blocking issue and three should-fix findings; all four are fixed in
  this branch (see below) and re-verified green.

## What shipped

`POST /api/catalog/export-save` (`internal/pages/import_page.go`) wrote the
catalog CSV via a raw `os.Create` into
`filepath.Join(os.UserHomeDir(), "Downloads", ...)`. On Android
(gomobile-embedded WebView) there is no user-visible, shared Downloads
folder reachable that way — `os.UserHomeDir()` resolves to the app's
private sandbox there (if it resolves at all), invisible to the user in any
file manager.

The fix, across three files:

1. **`internal/pages/import_page.go`** — no behavior change for desktop/Pi.
   Each of the endpoint's 3 failure branches (`os.UserHomeDir`,
   `os.MkdirAll`, `os.Create`) now logs *which step* failed
   (`logging.L().Errorf(...)`, ut-docs#1258's own AC) before showing the
   same generic `import.export_save_failed` notice as before —
   previously undiagnosable from logs alone. `os.MkdirAll`'s error, formerly
   discarded (`_ = os.MkdirAll(...)`), is now checked.
2. **`web/ui/pages/catalog.html`** — the export button gets a stable
   `id="catalog-export-btn"`. A new inline script: when
   `window.AndroidKiosk` is present (the same presence-check idiom
   `settings.html`'s `exitLockdown()` call already uses, ut-docs#1254 — a
   safe no-op on desktop/Pi/an ordinary browser), the button's click
   instead navigates to the pre-existing, already-correct
   `GET /api/catalog/export` (same `canPerform(d, r, "import_export")` gate,
   already sets `Content-Disposition: attachment`).
3. **`android/app/src/main/java/com/universaltill/pos/MainActivity.kt`** —
   registers `WebView.setDownloadListener` (Android's own documented
   mechanism for a WebView response carrying `Content-Disposition:
   attachment`) once, alongside the existing `KioskBridge`
   `addJavascriptInterface` call. The listener delegates to Android's own
   `DownloadManager`, forwarding the WebView's session cookie (needed
   because the download is a fetch of this same till's authenticated
   loopback origin) — the OS-supported way to land a file in the shared
   Downloads collection, rather than a new raw filesystem/MediaStore bridge.
4. **`internal/pages/export_save_notice_test.go`** — two new tests forcing
   the `os.UserHomeDir` and `os.MkdirAll` failure branches directly and
   asserting the logged detail names the specific step, via
   `logging.Recent()` / `logging.ResetRecent()`.

## Independent review — round 1 findings, and how each was resolved

The reviewer's own report (full detail in the PR/issue thread) found:

- **BLOCKER — `guard-docs-shots.sh` failed.** `web/ui/pages/catalog.html`
  is in the guard's hashed surface and `web/help/img/manifest.json` hadn't
  been regenerated. **Fixed:** `make docs-shots` regenerated the manifest;
  the catalog page's own screenshots came back byte-identical (the change
  is inside a `<script>` block, no visible rendering change) — only
  `manifest.json`'s surface hash moved. Four unrelated PNGs
  (`en/sell`, `fa/sell`, `ar/translations`, `fa/translations`) also
  regenerated with a few bytes of pure PNG-encoder AA jitter (the
  documented, accepted residual noise `e2e/tests-docs/docs-shots.spec.ts`
  itself notes) — discarded rather than committed, since they're unrelated
  to this diff and the guard hashes source surfaces, never PNG bytes.
- **Should-fix 1 — pre-Android-10 permission gap.** `minSdk = 24`, but
  `setDestinationInExternalPublicDir`'s `DESTINATION_FILE_URI` needs
  `WRITE_EXTERNAL_STORAGE` below API 29 (Q), which this app deliberately
  doesn't declare (a dangerous permission needing its own runtime-grant
  flow). Unpatched, `enqueue()` throws `SecurityException` on API 24–28 and
  the original `catch` swallowed it — silently reproducing the ticket's own
  bug on an older OS range. **Fixed:** branch on
  `Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q` — `setDestinationInExternalPublicDir`
  on Q+, `setDestinationInExternalFilesDir` (needs no permission at any API
  level; app-scoped external storage) below it. A real, working save on
  every supported OS version, not a silent failure on the older ones.
- **Should-fix 2 — kiosk mode hides the only feedback channel.** The
  original design relied entirely on Android's own download-completion
  notification. That's not reachable here: `onResume()` unconditionally
  engages immersive mode + the kiosk lock, `/catalog` isn't in the
  manager-facing exemption list `onPageFinished` checks, and under full
  Device-Owner Lock Task the OS suppresses notifications outright — so a
  successful (or failed) Android export could produce zero visible
  confirmation. **Fixed:** the click handler now also calls the page's
  existing global `renderNotice(el, level, text)` (`web/public/app.js`,
  the same builder `NOTICE_MSG`-style callers elsewhere in this file already
  use) into the existing `#export-msg` element, with a new locale key
  `import.export_download_started` (added to all four in-repo locales:
  en/ar/fa/tr — `guard-i18n.sh` green). Explicitly optimistic, not a
  success guarantee (the comment says so) — `DownloadManager.enqueue()` is
  fire-and-forget from `startDownload`, same honest limitation the OS
  notification itself always had.
- **Should-fix 3 — the original catch comment overclaimed.** It said "the
  OS surfaces the failure" for an exception from `enqueue()`/the `Request`
  builders — false: if nothing was ever enqueued, DownloadManager has
  nothing to report. **Fixed:** comment corrected to say so plainly.
- **Nit 1 — the original `hx-trigger="click[!window.AndroidKiosk]"` filter
  fails OPEN.** If htmx's expression compiler ever choked on it, htmx would
  fall back to firing the click unfiltered — on Android that would mean the
  broken POST *and* the new download *and* a misleading success notice, all
  at once. **Fixed:** dropped the `hx-trigger` filter entirely; the Android
  branch's own click listener now calls `preventDefault()` +
  `stopImmediatePropagation()` so htmx's own click listener on the same
  button (registered later — this plain, non-deferred inline script runs
  before htmx's `defer`-loaded one) never fires at all. Fails closed by
  construction, not by an expression staying syntactically valid.

## Independent review — round 1 checks that already passed clean

- `go build ./...`, `go vet ./internal/pages/...`, `gofmt -l` on both
  changed Go files, `go test ./internal/pages/...` (full package + the two
  new tests) — all green, independently re-run by the reviewer.
- `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-htmx-loaded.sh`, `guard-android-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-kiosk-engine.sh` — all green.
- **TDD claim independently re-verified by the reviewer**, not taken on
  faith: reverted only `internal/pages/import_page.go` to `HEAD~1`, the two
  new tests failed with exactly the claimed `got: []` (no logged Problem),
  restoring the file returned both to green.
- **Security posture:** confirmed the new `DownloadListener`/cookie-forward
  path introduces no new exposure — `shouldOverrideUrlLoading` already
  blocks any cross-origin navigation (main frame and subframes) before an
  untrusted page could ever load in this WebView, let alone trigger the new
  listener; `CookieManager.getInstance().getCookie(url)` only ever returns
  cookies scoped to that URL's own domain.
- Kotlin correctness checked by eye (see "Known, disclosed limitation"
  below for why not by compiler) — no errors found: import set, SAM lambda
  parameter count/order, nullability handling, `.apply` receiver method
  calls all check out.
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Re-verification after the round-1 fixes (this session, not the reviewer)

- `gofmt -l`, `go build ./...`, `go vet ./internal/pages/...`,
  `go test ./internal/pages/...` (full package) — all green again.
- Full guard sweep re-run clean: `guard-data-access.sh`, `guard-i18n.sh`
  (1303 keys resolve, all locales match `en.json`), `guard-compliance-claims.sh`,
  `guard-kiosk-engine.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`
  (23 topics × 4 locales fresh), `guard-htmx-loaded.sh`,
  `guard-android-i18n.sh`, `guard-android-status-address.sh`,
  `guard-webkit-version.sh`.
- **TDD claim re-verified a second time**, after the round-1 fixes: reverted
  `internal/pages/import_page.go`, confirmed both new tests fail with the
  same `got: []`, restored, confirmed green.
- **Real driven run of the app** (`e2e/seed_demo` + `go run .`,
  `UT_AUTH=off`, headless Chromium at `/opt/pw-browsers`) against the
  **desktop/browser path** (no `window.AndroidKiosk`) — the one path this
  cold cloud session can actually exercise end-to-end: navigated to
  `/catalog`, clicked the export button, confirmed the page's own
  `#export-msg` rendered the pre-existing `pos-notice success` markup with
  the destination path (`Saved to /root/Downloads/catalog-…csv`), byte-for-
  byte the same behavior as before this change, with zero browser console
  errors from the new inline script. Confirms the fix is additive for
  non-Android platforms, not a regression.

## Known, disclosed limitation — not silently skipped

This is a cold cloud cycle with **no Android device or emulator**, and the
Android Gradle Plugin **cannot be fetched at all**: the org egress policy
blocks `dl.google.com` (confirmed via the proxy's own status endpoint — a
genuine, non-retriable 403 policy denial, not a transient failure). So the
Kotlin change in `MainActivity.kt` was **not compiled or built** in this
cycle, by either the implementer or the independent reviewer — both
reviewed it for correctness by eye (imports, SAM lambda signature,
nullability, API usage) instead. No PR-time CI workflow builds the Android
app either (`./gradlew assembleRelease` appears only in
`.github/workflows/release.yml`), so this gap does not close at merge — a
genuine compile error, if one exists despite the eye-review, would first
surface when cutting a release. On-device verification ("actually reachable
from the Files app on a real Android tablet") is likewise out of reach here
— same class of gap as ut-docs#1078/#1281's own precedent for Android
hardware verification from a cold cloud cycle.

## Manual verification beyond automated tests

Covered above under "Re-verification after the round-1 fixes" — the
desktop/browser path was driven for real; the Android path's Kotlin was
reviewed by eye twice (implementer + independent Opus reviewer) but is
disclosed as unverified by any compiler or device, per the limitation
above.
