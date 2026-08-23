# Reset-archive purge: show per-row retention eligibility in the Settings → Data UI (ut-docs#698)

**Date:** 2026-08-23
**Card:** universaltill/ut-docs#698
**Complexity:** easy
**Repo/area:** `universal-till` — `internal/data`, `internal/pages`, `web/ui/pages/settings.html`, `web/locales/*`, `web/help/*/country-settings.md`

## What shipped

`POST /api/data/reset-archives/{id}/purge` already refused to permanently
delete a reset-archive batch still inside its country's statutory retention
window (ADR-0040/ADR-0042, ut-docs#661/#699) — but the Settings → Data →
Reset archives table rendered the "Delete permanently" button and its PURGE
confirm input unconditionally on every row, even for a batch that would
refuse for a decade under the ADR-0040 global floor. An operator could type
PURGE, confirm a "cannot be undone" dialog, and still get refused.

Fix, read-only end to end (no change to the enforcement path itself):

- `internal/data/reset_archive_repo.go`: extracted the pure
  `computeRetainedUntil(archivedAt, minDays)` helper from `DeleteResetBatch`'s
  existing inline midnight-truncation logic (ut-docs#699), and generalized
  `resolveArchiveMinDays` to accept the package's existing `queryRower`
  interface (`*sql.DB` or `*sql.Tx`) instead of `*sql.Tx` only.
  `DeleteResetBatch` keeps passing its own `tx` — behavior unchanged. `ResetBatch`
  gained `Purgeable bool` / `RetainedUntil time.Time`, computed in
  `ListResetBatches` using the exact same helper and minDays lookup
  `DeleteResetBatch` enforces against, resolved once per call (not per row).
  Fail-safe on both a minDays-lookup error and a (should-never-happen)
  `CreatedAt` parse failure: the row defaults to `Purgeable=false` rather
  than risking a false "yes" that a real purge attempt would still refuse.
- `internal/pages/data_api.go`: `GET /api/data/reset-archives` now returns
  `purgeable`/`retained_until` (snake_case, `retained_until` omitted when
  there's no gate — never a meaningless `0001-01-01`).
- `internal/pages/settings_page.go` / `web/ui/pages/settings.html`: a
  non-purgeable row shows a translated "Retained until <date>" message and
  hides the purge confirm-input/button entirely, rather than merely
  disabling it — an operator can't type PURGE, confirm the "cannot be
  undone" dialog, and only then get refused. A purgeable row (no sales, or
  past the window) renders exactly as before. Restore controls untouched.
- New locale key `settings.data.archives_retained_until_row`, real
  translations in all four `web/locales/*.json` (en/ar/fa/tr).
- `web/help/{en,ar,fa,tr}/country-settings.md`: one sentence added to the
  existing "Archive retention" bullet, noting the list now shows the date
  and hides the control itself; `make docs-shots` regenerated the affected
  screenshots (surface + topic hashes both moved, so every screenshot in
  the manifest was refreshed — expected, not scope creep).

Tests (TDD — written first, confirmed failing pre-fix): 4 new
`TestListResetBatches_*` cases in `reset_archive_delete_test.go` (zero-sales
always-purgeable, within-window not-purgeable, outside-window purgeable, and
a case mirroring #699's exact midnight-boundary shape that also asserts
`DeleteResetBatch` agrees with `ListResetBatches` on the same batch), two
new assertions/a new test in `data_api_test.go` for the JSON fields, and a
new `TestSettingsPage_ResetArchivesShowsPurgeEligibility` in
`settings_page_test.go` driving the real template render.

## Independent review

Fresh-context Sonnet subagent (per `complexity:easy` routing), isolated
worktree, adversarial pass plus its own verification runs (not just reading
the diff).

**Verdict: safe to merge, no blocking findings.**

Confirmed:

- `ListResetBatches`'s eligibility computation matches `DeleteResetBatch`'s
  enforcement in every case checked: zero-sales carve-out, the #699
  midnight-truncation boundary (both call the identical
  `computeRetainedUntil`), unknown-country fallback, and both fail-safe
  branches (minDays-lookup error, `CreatedAt` parse failure) — no case where
  the read path and the enforcement path could disagree.
- `resolveArchiveMinDays`'s signature widening to `queryRower` doesn't
  change `DeleteResetBatch`'s transactional consistency (it still passes its
  own `tx`), and doesn't collide with `demo_seed_repo.go`'s own use of the
  same package-level interface.
- No raw SQL introduced outside `internal/data` (`guard-data-access.sh`).
- Locale key present, non-empty, single `%s` preserved, and genuinely
  translated (not English copies) in all four locales — reviewer read the
  Arabic/Persian/Turkish text directly.
- The template's `{{ if .Purgeable }}` branch is well-formed and leaves the
  restore controls untouched on both branches.
- All CI-blocking guards relevant to this diff pass:
  `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-compliance-claims.sh`. `gofmt -l .`,
  `go build ./...`, `go vet ./...`, and the full `go test ./...` all clean.
  The pre-existing #699 regression tests
  (`TestDeleteResetBatch_RetainedUntilDateIsPurgeableFromMidnight`,
  `TestDeleteResetBatch_BoundaryEitherSideOfCountryWindow`, etc.) still pass
  unmodified — the refactor didn't change `DeleteResetBatch`'s own behavior.
- Money/offline-first: confirmed not applicable — no `money.Money` values
  and no checkout-path code in this diff.

**Independent TDD re-verification:** performed in a separate isolated `git
worktree` (the shared checkout is sandboxed against reviewer-session git
operations), overlaid with the uncommitted diff via `cp`. Reverted
`ListResetBatches` to leave `Purgeable`/`RetainedUntil` present but
uncomputed, keeping the new tests untouched: all 4 new
`TestListResetBatches_*` tests failed with real, meaningful assertion
errors (e.g. `"zero-sales batch must always be Purgeable, got {...
Purgeable:false RetainedUntil:0001-01-01 ...}"`), not compile errors or
unrelated mismatches. A stricter revert (removing the struct fields
entirely) produced real compile errors directly naming the missing fields
in `data_api.go`, `settings_page.go`, and the test file — confirming the
JSON/template consumers genuinely depend on the new fields, not just the
tests. Restored the fix (byte-for-byte, md5sum-verified), re-ran — all
green again.

Two non-blocking observations, not worth acting on: (1)
`resolveArchiveMinDays` still runs once per `ListResetBatches` call even
when every batch has zero sales (a trivial, already-commented-as-deliberate
cost); (2) confirmed (not a finding, a scope check) that `DeleteResetBatch`'s
own gate logic/semantics were correctly left untouched — the card's own
non-goal.

## Verified beyond automated tests

Drove the real app (a built binary, a scratch SQLite DB seeded with one
zero-sales batch and one gated batch, `UT_AUTH=off`) and took real
screenshots via the pre-installed Chromium at the 1024×900 kiosk viewport:

- **en, light theme**: purgeable row shows Restore + Delete permanently as
  before; gated row shows Restore + "Retained until 2036-08-20 — protected
  by your country's retention rules", no purge input/button, no
  overlap/misalignment.
- **fa (RTL)**: layout correctly mirrors, translated message reads
  correctly, purge controls correctly absent on the gated row, restore
  controls present on both.
- **tr (longest translation, 75 chars)**: wraps cleanly across 3 lines, no
  overflow into the row below.
- **dark theme, en**: rendered identically (the row reuses the page's
  existing `class="muted"` utility, unchanged elsewhere) — not independently
  screenshotted with a distinct dark-mode toggle path (the app's dark theme
  isn't a URL query param); accepted as a real-but-low-risk gap given the
  unchanged shared CSS class.
- `GET /api/data/reset-archives` JSON: confirmed `purgeable`/`retained_until`
  match the UI exactly (`retained_until` correctly absent on the zero-sales
  row).
- Server-side defense in depth: a raw `POST .../purge` against the gated
  batch still returns `409` with the existing refusal message — the UI
  change is additive, the enforcement gate is unchanged.

Not screenshotted: `ar` (same RTL code path as `fa`, already verified) —
accepted as a real-but-low-risk gap given `ar`'s translated string (56
chars) is shorter than both `fa` and `tr`, which were checked.
