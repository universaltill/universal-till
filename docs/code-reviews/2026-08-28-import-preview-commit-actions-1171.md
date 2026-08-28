# Code review — import page: repeat Import after preview, distinct commit success (ut-docs#1171)

- **Date:** 2026-08-28
- **Branch:** `fix/1171-import-preview-commit-actions`
- **Reviewer:** independent reviewer (fresh-context Sonnet subagent — `complexity:easy`)
- **Refs:** ut-docs#1171

## What shipped

The product owner, importing a real 217-item `.bkp` on the Pi till, was
stranded at the bottom of a long preview with no Import control in reach,
and after committing couldn't tell whether the import had actually happened
(the response read like just another preview).

- `internal/pages/import_page.go` — a preview response (`!commit`) now
  repeats the Import action after the grid: `type="submit" form="import-form"`,
  touch-target sized, form-associated to the page's own `<form id="import-form">`
  so it rides the same htmx multipart submit the top button already triggers.
- A commit response now renders inside a distinct block with an explicit
  "Imported N item(s) ✓" headline and a View catalog button.
- New locale keys `import.commit_success` / `import.view_catalog` added to
  all four shipped locales (en/ar/fa/tr).
- `web/help/*/catalog.md` updated (all four locales) to describe both
  behaviors.
- `web/public/app.css` — new `.notice-block-success` rule, same shape as the
  existing `.notice-block-warn`.
- `make docs-shots` re-run and committed (`app.css` is part of every
  screenshotted page's surface hash — `guard-docs-shots.sh` flagged it).
- Two new template-regression tests (`TestImport_PreviewEndsWithReachableImportButton`,
  `TestImport_CommitShowsDistinctSuccessSummaryWithCatalogLink`), both
  TDD-verified red-then-green against the pre-fix code.

## Review round 1 — findings and resolution

An independent fresh-context Sonnet subagent reviewed the diff without
implementer context. Verdict: **safe to merge, no blocking issues**, with a
prioritized list of non-blocking findings. Per this repo's `complexity:easy`
routing, one review round is the default; nothing here reached blocker class
(money/tax, data loss, security), so no second round was run.

**Addressed:**

- **Success banner shown regardless of real row failures.** The reviewer's
  strongest point: an unconditional green `.notice-block-success` reads as
  unambiguous success even when a row hit a genuine, non-duplicate failure
  (item/category/tax-code creation erroring — tallied in `failed` — as
  opposed to an expected "already in catalog" skip). That's false confidence
  in exactly the state this card exists to make trustworthy. **Fixed:** the
  block now renders `.notice-block-warn` (amber, the same treatment a
  preview issue already gets) instead of `.notice-block-success` whenever
  `failed > 0`; the summary text and View catalog link are unchanged. New
  test `TestImport_CommitWithRowFailuresUsesWarnBannerNotSuccess`,
  TDD-verified red-then-green (forces one row's item insert to fail via a
  SQLite trigger, same pattern as `TestImport_UnexpectedItemInsertFailureRollsBackWholeRow`).

**Accepted, not fixed (non-blocking, out of scope for this card's size):**

- `created == 0` (a fully-duplicate re-import) still renders "Imported 0
  item(s) ✓" — arguably accurate; a cosmetic edge case, not a correctness
  bug, and consistent with the existing (pre-this-diff) summary line's own
  wording for the same case.
- No real-browser (Playwright) test clicking the new bottom button — the
  Go-level template-regression tests satisfy the card's own acceptance
  criterion ("template regression test for both states") and are meaningful
  (they fail red on the pre-fix code); the mechanism itself (a
  `type="submit" form="import-form"` button rendered outside its `<form>`)
  has a proven precedent already in this codebase
  (`web/ui/pages/settings.html`'s `form="new-setting"` button against a bare
  `hx-post` form), so the incremental risk an e2e test would catch is low
  relative to its cost on an `easy`-tier card.
- No test for the non-interactive (`stagedFormID == ""`, staging failed)
  preview path — the bottom button is gated only on `!commit`, so it renders
  there too, but staging virtually always succeeds in the test harness (and
  in practice); a real gap, tracked rather than blocking this card.
- `web/help/img/fa/translations.png` changed as a side effect of the full
  `make docs-shots` re-run required by the `app.css` surface-hash change —
  confirmed unrelated to this diff (pre-existing intermittent
  text-rasterization drift the harness's own comments already document,
  ut-docs#930).
- Per `guard-docs-shots.sh`'s documented rule, only a topic's `routes[0]`
  is screenshotted; for `catalog.md` that's `/catalog`, not `/import` — so
  neither the new button nor the new banner is actually captured by any
  shipped screenshot. Pre-existing tooling gap (ut-docs#620), not something
  this change could address.
- `en.json` → `ut-plugin-language-{de,es}` follow-up: out of scope for this
  repo's diff; `lang-pack-drift` is advisory-only on this PR (only blocking
  on push to `main`) — owned by whichever lane merges this PR, per
  ECOSYSTEM.md's "lane that merges the core change owns the implied
  follow-up" rule.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` (full repo) | pass |
| `go test ./internal/pages/... -race` (scoped to `TestImport*`) | pass, 194s |
| `guard-i18n.sh` | pass — 1301 keys resolve, all locales match |
| `guard-data-access.sh` | pass |
| `guard-compliance-claims.sh` | pass |
| `guard-help-topics.sh` | pass |
| `guard-docs-shots.sh` | pass — surface fresh after `make docs-shots` |
| All other CI-blocking guards (migration-version-collision, kiosk-engine, plugin-menu-read, webkit-version, kiosk-launch-flags, android-status-address, android-i18n, emoji-font, htmx-loaded, autofill-suppression, osk-loaded, makefile-version, check-brand-assets) | pass |

### Independent re-verification of both TDD claims

Reverted only the two production diffs (bottom-button rendering, then
separately the `successClass` gating) with the new tests left in place;
confirmed the package still compiled (so failures were genuine assertion
failures, not build errors); re-ran the affected tests — all failed
on-topic (`must repeat a form-associated submit control after the grid`,
`must render a visually distinct success block`, `must not show the
unqualified success banner`). Restored the fix; all tests returned to
green.
