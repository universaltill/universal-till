# Code review — dead pos.tax_inclusive seed removal + transactional SaveRuntimeConfig

- **Date:** 2026-07-31
- **Task:** ut-docs#12 (Coverage batch 11 follow-ups)
- **Branch:** `fix/ut-docs-12-tax-inclusive-seed-tx-save`
- **Author:** pipeline Dev step (Fable)
- **Independent reviewer:** general-purpose subagent on **Opus** (different model, per standing practice)

## What shipped

1. **Migration `022_remove_dead_tax_inclusive_seed.sql`** — deletes the
   `pos.tax_inclusive` settings row seeded by `001_init.sql:564`. The key was
   confirmed dead ecosystem-wide (grep): every live path uses
   `store.tax_inclusive` (`pages/common.KeyTaxInclusive`). 001 is released and
   append-only, hence a follow-up migration.
2. **`SettingsRepo.SetMany(ctx, map[string]string)`** (`internal/data/settings_repo.go`)
   — tx-wrapped upsert of all pairs in sorted-key order, `settingsObs` idiom,
   empty-map fast path. `Store.SetMany` passthrough; **`SaveRuntimeConfig` now
   makes one `SetMany` call** instead of 7 sequential non-transactional `Set`s,
   so a mid-way failure no longer persists a prefix of old/new mixed values.

## TDD evidence (independently re-verified, not just claimed)

All three regression tests were written first and failed against the old code
with the real symptom (`store.currency = "EUR" after failed save`,
`pos.tax_inclusive still present (n=1)`). The reviewer **mutation-tested**
them independently: made `SetMany` non-transactional → both atomicity tests
failed; removed migration 022 → both dead-seed tests failed. The pipeline also
re-verified by stash-reverting the fix and re-running before the review.

## Verified beyond automated tests

- Real-app smoke: built the binary, booted on a scratch port/data dir — clean
  boot, `settings` contains only `store.tax_inclusive`, `schema_migrations`
  records version 22; process killed same-turn.
- Upgrade path: `TestDeadTaxInclusiveSeedRemovedOnUpgrade` rewinds
  `schema_migrations` so 022 alone re-runs against a DB that still has the
  row (reviewer confirmed the rewind genuinely exercises the runner's
  `MAX(version)` gate).
- Full `go test ./...`, `go vet ./...`, both CI guards — green.

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | should-fix | `pages/common.SaveState` (the real settings-page save path) is still 11 non-tx `Set`s with discarded errors — same bug class, user-facing | **Deferred → ut-docs#157** (needs error-surfacing signature change; out of #12's scope) |
| 2 | nit | Atomicity tests' trailing assertion (`theme`) sorted after the abort key, so it was tautological | **Fixed** — asserts `store.currency_symbol` (sorts before the abort) |
| 3 | nit | Upgrade test setup would fail on UNIQUE violation instead of the real assertion if 022 regressed | **Fixed** — `INSERT OR REPLACE` |
| 4 | nit | `SetMany({})` took a write tx for zero work | **Fixed** — early return |
| 5 | nit | `newSettingsTestDB` hand-rolled schema laxer than 001's (pre-existing style in that file) | Accepted — matches existing convention |

Also checked clean: no SQLITE_BUSY/deadlock risk (deferred tx, first stmt is a
write; only prod caller runs at boot before sync goroutines start); replica
re-seed from a pre-022 primary can harmlessly re-add the dead row (nothing
reads it); no file writes (MkdirAll/paths.Data classes n/a); no i18n/money
surface; no real shop names or secrets in test data.

## Verdict

**Safe to merge.** Nothing blocking; all should-fix/nit items either fixed
in-branch or carded (#157).
