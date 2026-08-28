# Code review: import page reachable Import/View-catalog controls (ut-docs#1171)

**Date:** 2026-08-28
**Card:** ut-docs#1171 — "Import page: commit button only exists at the top of
the page — after a 200-row preview the operator strands at the bottom with no
Import/next action."
**Branch:** `fix/1171-import-reachable-controls`

## What shipped

The catalog import page (`web/ui/pages/import.html` + `internal/pages/import_page.go`)
only offered its primary action (Import) at the top of the page, next to the
file field. A long preview or commit result table (up to 200+ rows, plus the
export card below it) left the operator scrolled to the bottom with no
reachable action — reported by the product owner importing a real 217-item
`.bkp` file on the Pi till.

- **Preview**: the Import button is now repeated after the results table,
  form-associated (`form="import-form"`) since it renders into `#import-result`,
  outside `<form id="import-form">` — same pattern the problem-grid's own
  controls already use. Works identically whether the upload staged
  successfully or not (the fallback path re-reads the raw uploaded file).
- **Commit**: the summary is now wrapped in a new `.notice-block-success`
  block (green, mirrors the existing `.notice-block-warn`), with a
  **View catalog** link — shown both at the top (immediate feedback) and
  repeated after the table (so a long commit result doesn't leave the
  operator stranded the same way).
- New locale key `import.view_catalog` added to all four locales
  (en/ar/fa/tr).
- `web/help/{en,ar,fa,tr}/catalog.md` updated with a bullet describing the
  new behavior; screenshots regenerated via `make docs-shots`.
- Two new template regression tests:
  `TestImport_PreviewEndsWithReachableImportControl`,
  `TestImport_CommitEndsWithSuccessSummaryAndCatalogLink`.

## Independent review

A fresh-context Sonnet subagent reviewed the diff adversarially, in an
isolated worktree, with no visibility into the implementation discussion.
**Verdict: SAFE TO MERGE, no blockers.**

What it verified directly (not taken on trust):
- `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — all clean.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job
  (`guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`,
  `guard-i18n`, `guard-compliance-claims`, `guard-docs-shots`,
  `guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `check-brand-assets`,
  `guard-makefile-version`) — all pass.
- **TDD claim, re-verified independently**: reverted `import_page.go` and
  `app.css` to `main` (kept the new tests), confirmed both new tests fail
  with real assertion errors (not compile errors), restored the fix,
  confirmed both pass again.
- Read the full handler diff: every early `return` in the handler happens
  before the table/bottom-control block, and the currency-confirm detour is
  a separate function that returns before reaching it — no path double-
  renders the trailing control or skips the table while still emitting it.
- Confirmed the repeated Import button works identically whether staging
  succeeded or silently failed (both paths reuse the same `<form
  id="import-form">`, browser-held file input).
- Checked all four locale translations for `import.view_catalog` — genuine,
  non-garbled, structurally consistent with the English.
- Checked no real client/shop name and no secret-shaped literal in the diff.
- Confirmed the WIP commit's author is `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>`, not an AI identity.

**One non-blocking nit**: `.notice-block-success`'s colors are hardcoded
`rgba(22, 163, 74, …)` rather than derived from `var(--success)` — matches
the pre-existing convention in the same file (`.notice-block-warn`,
`.pos-notice.success`), not a new problem this diff introduced. Not worth
blocking on; left as-is for consistency with neighboring code.

## Verified beyond automated tests

Drove the real app (Go server + Playwright/Chromium) with a 210-row CSV:
- **English, light theme**: preview ends with a reachable "Import" button
  after the table; commit ends with a green "View catalog" box, both at the
  top and repeated after the table.
- **Farsi (RTL), light theme**: same behavior, correctly mirrored — button
  and box render right-aligned, `dir="rtl"` respected throughout, no
  logical-property regressions.
- Did not separately check dark theme — the new CSS class is a direct
  mirror of the already-shipped, already-theme-tested `.notice-block-warn`
  pattern (same `var(--radius)`, no light-only assumptions beyond the
  `rgba()` overlay technique already in production use), so it inherits
  that behavior rather than introducing new risk.

## Deferred / out of scope

- The pre-existing "… N more" row-count note (`… %d more`) is still a
  hardcoded English literal, not translated — a pre-existing i18n gap this
  card didn't introduce and wasn't in scope to fix.
- The hardcoded-color nit above, deferred for consistency with the
  surrounding block-notice pattern rather than fixed in isolation.
