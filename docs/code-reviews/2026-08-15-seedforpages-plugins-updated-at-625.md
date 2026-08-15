# Code review — seedForPages plugins.updated_at drift (ut-docs#625)

**Date:** 2026-08-15
**Card:** universaltill/ut-docs#625 (p3, `complexity:easy`)
**Branch:** `fix/seedforpages-plugins-updated-at-625`
**Dev:** inline (Sonnet, easy-tier build model)
**Reviewer:** independent subagent, fresh-context Sonnet (easy-tier review — a
clean-context instance that never saw the dev reasoning, per the pipeline's
model-routing rule)

## What shipped

Root cause was already confirmed by the ticket: `internal/pages/ui_smoke_test.go`'s
`seedForPages` test helper hand-rolls a partial `plugins` table missing the
`updated_at` column production's real schema has
(`internal/db/migrations/001_init.sql`:
`updated_at TEXT NOT NULL DEFAULT (datetime('now'))`). `data.PluginRepo.
GetPluginVersionAt` queries `WHERE id = ? AND updated_at <= ? ORDER BY
updated_at DESC`, called from `loadReceiptLegalBlocks` on the real tender
path whenever `completedAt` is non-zero — any test using `seedForPages` that
exercises that path hits `SQL logic error: no such column: updated_at`
(silently, since the tender path's caller discards the returned error — it
only ever showed up as log noise, per the original bug report).

- `internal/pages/ui_smoke_test.go`: added `updated_at TEXT NOT NULL
  DEFAULT (datetime('now'))` to `seedForPages`'s `plugins` CREATE TABLE
  (column-identical to production), plus a new regression test
  `TestPluginRepoGetPluginVersionAt_SeedForPagesSchema` that calls
  `data.NewPluginRepo(db).GetPluginVersionAt` directly against the seeded
  fixture and asserts success — a hard assertion on the exact defect,
  rather than the log-only symptom the original bug report hit.
- **Found while verifying the fix, fixed in the same change**:
  `internal/pages/fiscal_sign_hook_test.go` had two tests
  (`TestFiscalSignExclusivity_EnableFailsClosedOnDBError`,
  `TestFiscalSignExclusivity_EnableSucceedsWithoutConflict`) carrying their
  own ad-hoc `ALTER TABLE plugins ADD COLUMN updated_at TEXT` workaround for
  this exact gap, with comments explicitly naming it. Once `seedForPages`
  carries the column natively, those `ALTER TABLE` calls fail with
  `duplicate column name: updated_at` — removed the now-redundant
  workaround (and stale comments) from both tests.
- `internal/pages/plugin_api_test.go`: touched up a neighbouring comment on
  `openRealSchemaPagesDB` that cited `plugins.updated_at` as an example of
  what the simplified fixture lacks — half-stale after this fix — to cite
  `installed_from_url`/`installed_sha256` instead (still genuinely missing,
  still the real reason that helper exists).

## Independent review (fresh-context Sonnet) — 0 blockers, 1 nit, fixed

Re-derived the defect independently rather than trusting the diff: reverted
just the `updated_at` column addition and reproduced the exact original
error (`no such column: updated_at`) on the new regression test, restored
it and confirmed green — proving the test fails/passes for the right
reason, not by accident. Independently ran the two `fiscal_sign_hook_test.go`
tests after the `ALTER TABLE` removal (both pass) and confirmed via each
test's own (now-removed) comment that the `ALTER TABLE` was purely a
schema-gap workaround, not behaviourally significant setup. Ran the full
`internal/pages` package (`105s`, all green — no `-race`, see below), `go
build ./...`, and all three required guards
(`guard-data-access.sh`/`guard-kiosk-engine.sh`/`guard-plugin-menu-read.sh`)
— all clean. Grepped `internal/pages/**_test.go` for any other
`updated_at`-shaped workaround for this same gap — none found (the one
other `ALTER TABLE plugins` hit, in `plugins_page_test.go`, is an unrelated
gap in a separate, independent fixture that never touches this code path).
Diff-checked every column the fixture defines against `001_init.sql`
type-for-type per the ticket's own acceptance criterion — no other drift.

**Nit, fixed** — `plugin_api_test.go`'s `openRealSchemaPagesDB` comment
cited `plugins.updated_at` as an example of what `seedForPages` lacks; now
half-stale since this fix. Updated to cite the columns that genuinely are
still missing (`installed_from_url`/`installed_sha256`).

## Known, unrelated pre-existing finding (not this ticket's scope)

`go test ./internal/pages/... -race` (single run, no repetition) hangs at
~575-600s in *this sandbox specifically* — reproduced independently, traced
to a stack stuck mid-commit inside SQLite's pager, inside the newly-merged
(same day, PR #365) `role_permissions`/`permission_actions` seeding loop in
`seedForPages`, unrelated to the `plugins` table this fix touches. Real CI
(GitHub Actions, which does run with `-race`) passed cleanly and quickly on
that same merge commit (`build`/`Test` steps green in ~2.5 min total), so
this reads as sandbox-specific resource contention under `-race` rather
than a real product defect — the same contention class already tracked by
ut-docs#674, just a new manifestation. Not investigated further here (out
of this card's easy-tier scope); worth a note on ut-docs#674 or a fresh
card if it turns out to reproduce in CI too. Verification for this ticket
was done without `-race` on the full package (clean, 105s) plus targeted
`-race`-free and default runs of the specific regression + previously-
workaround-carrying tests, matching the ticket's own acceptance criterion
in spirit ("or similar repeated-run pressure") without chasing an
unrelated, already-tracked class of flake.

## Verification beyond the automated suite

- `go test ./...` (whole repo, no `-race`): all green, ~86s.
- Confirmed the exact original error message reproduces on a reverted
  fixture and disappears after the fix — not inferred, directly observed
  twice (once by the dev, once independently by the reviewer).

## Safe-to-merge verdict

Yes.
