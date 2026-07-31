# Code review — Windows update messaging confirmed working as designed (board #152)

- **Date**: 2026-07-31
- **Branch**: `fix/windows-update-messaging-confirmed` → PR (this change)
- **Card**: ut-docs #152 — field report, a friend on Windows v0.2.14 clicked
  Update and got a browser download page instead of an in-place update, "like
  the Pi fix in #147."

## Investigation (BA/Architect pass, before writing any code)

Traced `internal/selfupdate.supportedFor(exe, goos)`: the `windows` branch has
returned `false` ("updates via the installer") since the file's earliest
history in this repo (`c37cc6f`, 2026-07-28), unchanged through `fc5117a` and
the #147 refactor (`f828423`) that turned the `if` chain into a `switch`.
**This predates v0.2.14 and is unrelated to version — not a regression.**
Windows has never had in-app self-update; `Apply()` only knows how to swap a
`.tar.gz` archive (unix) or replace a `.dmg` bundle (`applyMacApp`, darwin) —
no such mechanism exists for overwriting/relaunching a currently-running
Windows `.exe`, which is a materially different problem than the
directory-writability question `dirWritable()` answers for the linux `/opt`
kiosk case #147 fixed.

`internal/pages/update_api.go`'s `updateUnavailableHTML` already deliberately
gives Windows an actionable download link (vs. a plain "unavailable" message
on a unix kiosk, per #147/#148, same day) — correct and already tested
(`TestUpdateFallbackHTML`).

**Conclusion: no updater behavior to fix.** Building a real Windows in-app
updater (silently re-running a downloaded Setup.exe, coordinating the desktop
shell's child-process lifecycle across the swap, no `.tar.gz`/`.dmg`
equivalent asset today) is a genuinely bigger, undesigned feature — logged as
a new Backlog card, not attempted here (matches the "what one item is allowed
to cost" scoping rule).

## What actually shipped

Two small, real gaps found while verifying the above, both fixed:

1. **Test-only, `internal/selfupdate/selfupdate_test.go`**: `TestSupportedFor`
   only exercised a `C:\Program Files\...` windows path. Added the actual
   shipped installer's per-user `%LOCALAPPDATA%` path
   (`packaging/windows/installer.nsi:31`, `RequestExecutionLevel user`) and a
   portable-zip-extraction path, so the windows exclusion is pinned against
   the real install shapes and can't quietly narrow to "windows, unless the
   dir is writable" the way the linux `/opt/unitill` carve-out works for a
   different (and here, inapplicable) reason.
2. **`web/ui/layouts/base.html`'s status-bar chip** — the thing the reported
   user actually clicked, not the Settings-page snippet: both the in-app
   "Update now" button and the download-page link render as the identical
   `sb-item sb-update` pill with the same up-arrow, distinguished only by
   "Update now" vs. "Update available" — nothing said the link leaves the
   app. Added `— Download` (reusing the existing `settings.update.download`
   key, already present in all 4 locales) so the chip itself states what
   clicking it does, matching the wording the Settings page already used.
   New test: `TestBaseLayoutUpdateChipSaysDownloadWhenSelfUpdateUnsupported`
   (`internal/httpx`), TDD — confirmed red against the pre-fix template
   (asserted failure message quoted the un-fixed chip text), then green.

## Independent (opus) review — real findings, both addressed

The reviewer confirmed the "not a regression, don't build a Windows
self-updater now" conclusion, but found:

- **BLOCKER-class (fixed): the two new `selfupdate_test.go` cases used
  double-backslash raw-string literals** (`` `C:\\Users\\...` ``, matching the
  pre-existing Program Files case's same flaw), so the literal didn't
  represent a real Windows path. Mutation-tested: a plausible bad future
  change (`if strings.Contains(exe, `AppData\Local`) { return true }`, i.e.
  someone "fixing" #152 by extending the linux writability carve-out to
  Windows) was **not caught** by the double-backslash version — the suite
  stayed green. Fixed to single backslashes (correct for a Go raw string) for
  all three windows cases; re-ran the same mutation and confirmed it's now
  caught (see verification below).
- **Real, separate bug surfaced (not fixed here, logged as a new card)**: the
  status-bar chip's `{{ else }}` branch has no OS/writability awareness of
  its own — a Pi kiosk with a root-owned `/opt/unitill` (exactly the
  mis-provisioned case #147's `dirWritable` now correctly rejects) still
  renders a clickable website link in the footer on every page, the same
  dead-end #147 claimed to close, because that fix only touched
  `update_api.go`'s Settings-page snippet, not `base.html`. Out of scope for
  this card (needs threading GOOS/a "why not" reason into the template
  context, not a copy tweak) — new Backlog card opened.
- Minor, not confirmed (can't test on real Windows): the chip's anchor has no
  `target="_blank"`; in the WebView2 desktop shell a plain link may navigate
  the till window itself away with no back affordance. Flagged in the new
  Backlog card alongside the kiosk dead-end, since both need the same "decide
  what this link should do per shell" design pass.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l` on every touched file — clean.
- `go test ./internal/selfupdate/... ./internal/httpx/... ./internal/pages/...`
  and the full `go test ./...` — all green except
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, confirmed
  **pre-existing and unrelated** (fails identically on a clean `origin/main`
  checkout with no diff applied; this sandbox runs as uid 0, so the test's
  `chmod 0555` read-only-dir probe doesn't actually block root — the same
  root-in-CI hazard `TestDirWritable`'s own comment already documents
  avoiding for this file's neighboring test).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
  both green (i18n guard's resolved-key count went 780 → 781: one more
  template call site for the already-existing `settings.update.download`
  key, no new key).
- Mutation re-verified personally (not just taken on the reviewer's word):
  reintroduced a windows-writability carve-out in `supportedFor`, confirmed
  `TestSupportedFor` fails with the exact new LOCALAPPDATA case, reverted,
  confirmed green again.
- TDD confirmed for the `base.html` fix: the new `httpx` test was written and
  run against the pre-fix template first (failed with the un-fixed chip text
  in the assertion message), then the template was fixed, then re-run green.
- No real client/shop name or secret-shaped literal in the diff.

## Deferred (new Backlog cards)

1. Status-bar `sb-update` chip in `base.html` shows a website link with no
   GOOS/writability awareness — reproduces the #147 kiosk dead-end outside
   the Settings page it was fixed in; also decide `target="_blank"`/shell
   navigation behavior for the WebView2 desktop shell.
2. A real Windows in-app self-updater (silent Setup.exe re-run + desktop
   shell coordination) — a genuine feature needing its own design, not a
   bug-card scope.

## Safe to merge

Yes — test-only + a one-key copy change to an existing, already-i18n'd
string; full gate green; independent review's one real defect (the escaping)
fixed and re-verified; the one deferred finding is a distinct, separately
logged bug, not a blocker for this card's own claim (Windows behavior is
correct and now more clearly communicated).
