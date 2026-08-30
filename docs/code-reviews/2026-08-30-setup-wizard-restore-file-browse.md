# Code review: wizard restore step — real Browse/Preview upload (ut-docs#1168)

**Date:** 2026-08-30
**Branch:** `fix/1168-setup-wizard-restore-file-browse`
**Reviewer model:** Opus (independent, fresh context, via a background subagent)
**Author/Dev model:** Sonnet (this pipeline cycle's session model — card labelled `complexity:medium`)

## What changed

The setup wizard's "moving from another till system?" step let an operator
declare CSV/Excel intent but never actually pick a file, and its own copy
claimed no dedicated `.bkp` import existed even though universal-till#501
shipped one — both reported directly by the product owner on the real Pi 5
till.

- Step 6's `csv_excel` panel now offers a real file Browse + Preview,
  reusing `/api/import`'s existing preview pipeline (`catimport`, staging,
  problem grid, `ut-docs#970` currency-confirm) byte-for-byte via a shared
  `id="import-form"`, rather than duplicating any of it.
- `POST /api/import`'s auth gate now also allows an **anonymous, preview-only**
  request during `NeedsFirstBoot` (no admin account yet), mirroring the
  existing exemption for `POST /api/setup/join` and friends.
- `commitStagedImportForSetup` (`import_stage.go`) replays a wizard preview
  as a real commit from inside `POST /api/setup`'s handler, once
  country/currency are saved and the admin session exists — one in-process
  `ServeHTTP` call into the unwrapped mux, `auth.WithUser` standing in for
  the auth middleware that call bypasses.
- `setup.restore.no_plugins_yet` (now false) removed from all 4 locales;
  both the wizard panel and the manual import page source their capability
  copy from the single `import.help` key.
- `web/help/en/users.md` (+ `ar`/`fa`/`tr`) updated; `make docs-shots`
  re-run.

## Independent review — round 1 verdict: NOT SAFE, 3 blockers

An Opus subagent reviewed the full diff (working from a reconstructed patch
after a mid-review TDD revert/restore on this session's own working tree
raced it — noted and not a real defect; see "process note" below). Findings,
most severe first:

1. **BLOCKER** — `/import?staged_id=` fallback form lacked
   `enctype="multipart/form-data"`; htmx posts urlencoded otherwise, and
   `POST /api/import`'s first line (`r.ParseMultipartForm`) 400s on that —
   the entire fallback path silently did nothing.
2. **BLOCKER** — `commitStagedImportForSetup` unconditionally supplied
   `confirm_currency`, silently answering `ut-docs#970`'s currency-confirm
   gate on the operator's behalf even when they never touched the
   pre-filled (OS-guessed) country/currency step — mislabelling every
   imported price under a guessed currency AND permanently marking the
   till's currency "confirmed", suppressing the real prompt for every
   future manual import too.
3. **BLOCKER** — `guard-docs-shots.sh` failed (screenshots not regenerated
   before the review's snapshot).
4. **SHOULD-FIX (near-blocker)** — the reused preview fragment's
   problem-grid/barcode-opt-in controls and its own bottom "Import" button
   render in the wizard but do nothing there (no `#import-commit` element)
   or are silently discarded by the auto-commit (which only forwards
   `commit`/`staged_id`/`confirm_currency`) — an operator who corrects a
   broken row in the wizard's preview would see it silently dropped.
5. **SHOULD-FIX** — the widened auth gate permitted an *anonymous commit*
   (not just preview), which the wizard's own UI never needs — unnecessary
   attack surface in the first-boot window.
6. **SHOULD-FIX** — only `en/users.md` was updated; `ar`/`fa`/`tr` still
   described the old behaviour.
7. **SHOULD-FIX** — `en/users.md` promised "no extra click needed", stronger
   than what a best-effort auto-commit actually guarantees.
8. **SHOULD-FIX** — no `htmx:responseError` handler on the wizard's preview
   form; a rejected/unrecognised file failed silently (empty result, no
   feedback).
9. **NIT** — an abandoned wizard preview (operator clicks Back) leaks its
   staged temp file until the registry's 1h TTL prune.
10. **NIT** — `/catalog?imported=1` was written but nothing reads the
    `imported` param.

Concurrency, the state-save-before-auto-commit ordering, and
`auth.WithUser` reaching `canPerform` through the bare mux were all
independently verified correct — no changes needed there.

## Fixes applied (round 2, this session)

| # | Fix |
|---|---|
| 1 | Added `enctype="multipart/form-data"` to `import.html`'s staged-only form. |
| 2 | `commitStagedImportForSetup` is now only attempted when the operator actually touched country/currency (`currencyTouched`, the same signal `ut-docs#970`'s own confirm-flag already uses) — otherwise the wizard falls straight to `/import?staged_id=...` so the real confirm prompt fires. Added a response-body check (`href="/catalog"`) as defense in depth against a 200 that isn't actually a commit (the confirm-prompt path is also a 200). |
| 3 | `make docs-shots` re-run after every subsequent edit; guard verified green. |
| 4 | Added a `wizard=1` form field the wizard's upload panel sends; `POST /api/import` now suppresses the problem-grid controls, the barcode opt-in checkbox, and the repeated bottom Import button whenever it's set — none of them are safe to act on mid-wizard. The full interactive pipeline is untouched on a normal (non-wizard) preview. |
| 5 | The first-boot exemption now covers **preview only** — `commit=1` is checked (after the multipart parse, to avoid widening the parse size cap by triggering an early `r.FormValue` call) and denied with 403 even during first boot if reached via the exemption path. |
| 6 | Translated the updated walkthrough paragraph into `ar`/`fa`/`tr`. |
| 7 | Softened `en/users.md` (and the 3 translations) to describe the best-effort fallback honestly instead of promising "no extra click needed" unconditionally. |
| 8 | Added an `htmx:responseError` handler on `setup.html` (same pattern as `shifts.html`), surfacing a rejected file's translated error message inline. |
| 9 | **Deferred** — bounded by the existing 1h TTL prune; cosmetic per the reviewer's own assessment, out of this card's scope. |
| 10 | Dropped the unused `?imported=1` query param — redirects to plain `/catalog`. |

New/updated tests (`internal/pages/setup_restore_import_test.go`), 9 total:
first-boot preview allow/deny, staged-preview auto-commit on wizard finish
(now also asserting `currency_confirmed` ends up `true`), auto-commit
skipped + no confirm-flag set when currency was never touched (fix #2's
regression test), anonymous `commit=1` denied even during first boot (fix
#5's regression test), interactive controls suppressed under `wizard=1` but
still offered on a normal preview (fix #4's regression test), the
`/import?staged_id=` multipart-vs-urlencoded round trip (fix #1's
regression test, reproduces the original 400 before asserting the fix),
staged-preview replay-failure fallback, and the staged-form GET rendering.

TDD claim independently re-verified for round 2's fixes the same way as
round 1: reverting the three production Go files + both templates to the
prior commit turns the 4 new fix-specific tests red (the 5 pre-existing
ones stay green, as expected — they test behaviour round 2 didn't touch);
restoring returns all 9 to green.

## Verification after fixes

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` (whole repo) — all packages pass.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally and green: `guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots` (after `make docs-shots`), `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`,
  `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`,
  `check-brand-assets`, `guard-makefile-version`.
- CI on the pushed branch: see the PR's own checks.

## Process note

Round 1's review subagent ran concurrently with this session's own
TDD revert-then-restore verification of the *first* commit (a legitimate,
short-lived revert of the tracked files to prove the original tests fail
without the fix, per this pipeline's standing TDD-reverification practice)
and its `git status` snapshot landed mid-revert. It correctly flagged this,
reconstructed the full diff from what it had already read, and reviewed
that reconstruction instead — its findings are against the real diff, not
an artifact of the race. Filed as a nudge for `reviewer`/`dev` SKILL.md: a
revert-then-restore TDD check and a review subagent sharing one working
tree can still cross a review's `git status` the same way ut-docs#386
worried about a commit crossing a subagent's edits — worth an explicit
"reviewer subagents get their own worktree/clone, same as the TDD
verification step already does" rule, not just the commit-timing one.

## Outstanding, accepted for follow-up (not blocking this card)

- Finding 9 (staged-upload leak on wizard Back) — cosmetic, bounded by the
  existing 1h TTL prune.
