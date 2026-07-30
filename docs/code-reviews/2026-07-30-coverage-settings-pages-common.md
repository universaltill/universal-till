# Coverage batch 11: `internal/settings` + `internal/pages/common`

**Date**: 2026-07-30
**Branch**: `test-coverage-settings-pages-common`
**Author**: autonomous SDLC pipeline (Sonnet)
**Independent review**: opus (different model, subagent)

## What shipped

Continuation of the `ut-docs/QUEUE.md` test-coverage push
(`internal/plugins` → `pages/catalog` → `data` batches 7–9 →
`cloudsync`/`config` batch 10). The queue's own note named three 0%
micro-packages as the next candidates: `internal/app`,
`internal/settings`, `internal/pages/common`.

**`internal/app` was scoped OUT of this batch** — it's already at
**79.7%** coverage from PR #101's unrelated shutdown-join fix
(`app_test.go` exists and covers `drainBackgroundServices`/`Run`'s
early-error path). The queue note calling it a 0% candidate was stale;
this is now corrected in the closing `QUEUE.md` entry.

**`internal/settings`**: 0% → **84.6%**. Covers the thin `Store`
wrapper (`Get`/`Set`/`All`) and `LoadRuntimeConfig`/`SaveRuntimeConfig`
— the boot-time config↔DB-settings mapping (`internal/app/app.go:86-87`
calls both back-to-back on every start).

**`internal/pages/common`**: 0% → **100.0%**. Covers `Deps.CurrentState`/
`UpdateState`/`SyncPrimaryURL`, and `state.go`'s `LoadState`/`SaveState`/
`BuildMenu`.

## Real bugs found and fixed, TDD-first

1. **`LoadRuntimeConfig` currency_symbol mis-gated** (`runtime.go:26`,
   originally): gated the `CurrencySymbol` assignment on
   `if curr != ""` (the *currency* value) instead of
   `if curr_symbol != ""` — a currency_symbol-only override (currency
   itself unset) was silently dropped. Regression test written to fail
   against the original code (confirmed), then fixed.

2. **`SaveRuntimeConfig` wrote `tax_inclusive` to the wrong key**
   (`runtime.go:64`, originally `"pos.tax_inclusive"`): `LoadRuntimeConfig`
   (and `internal/pages/common`'s `KeyTaxInclusive`, the real
   settings-admin UI's read/write key) both read `"store.tax_inclusive"`
   — so a value written by `SaveRuntimeConfig` at boot was never read
   back by anyone. Fixed to `"store.tax_inclusive"`. This closes a
   known loose end flagged in `2026-07-14-first-boot-wizard.md` and
   `2026-07-14-receipt-printing.md` (found independently by the
   reviewer, not previously connected to this code path).
   `internal/db/migrations/001_init.sql:564` separately seeds
   `pos.tax_inclusive='false'` as an initial default row — that row is
   now confirmed orphaned (nothing ever read it, before or after this
   fix); left in place since migrations are append-only and the row is
   inert. Logged as a follow-up in `QUEUE.md`.

3. **`LoadState` silently discarded the cfg default on a garbage
   stored value** (`state.go`, `TaxInclusive`/`AllowNegativeInventory`
   only) — found by the independent reviewer, not the author.
   `st.X, _ = strconv.ParseBool(v)` left the field at Go's zero value
   (`false`) on a parse error instead of keeping cfg's default,
   inconsistent with every sibling field (`TaxRate`/`UIScale`/
   `IdleLock`/`KioskIdleReset`), which all guard on `err == nil` and
   leave a pre-set default untouched on failure. Pre-existing bug, not
   introduced by this batch. Fixed to match the sibling pattern
   (pre-set `TaxInclusive` from `cfg.Locales.TaxInclusive` in the
   `RuntimeState` literal, only overwrite on successful parse).
   Verified failing-then-passing against both the original code and
   the fix.

## Independent review (opus) — verdict: SAFE TO MERGE WITH FIXES

Ran the full gate itself rather than trusting the author's report:
`go build`/`go vet` clean; `go test -race -count=1 -cover` green at the
claimed 84.6%/100.0% (verified via `go tool cover -func`); both CI
guards pass; full `go test ./... -race -count=1` green except one
**pre-existing, unrelated** failure
(`internal/issuereport TestSaveCleansUpDirectoryOnWriteFailure`,
root-in-container bypasses the read-only-directory check the test
relies on) — the reviewer independently confirmed this via a fresh
`git worktree` on unmodified `main`, same failure.

Independently re-verified both original bug-fix claims by reverting
each production line, confirming a real assertion failure (not a
compile error), then restoring and confirming green again.

**Findings, all addressed**:
- **F1/F2 (medium)** — the same seeded-default blind spot the author
  had already caught and fixed once (for `currency_symbol`) was still
  present in three other tests: `TestLoadRuntimeConfig_
  EmptyStoreKeepsDefaults`, `TestLoadState_DefaultsWhenStoreEmpty`, and
  (transitively) the unparsable-values test — `001_init.sql` seeds
  `store.name`/`store.currency`, which coincide with `baseCfg()`'s
  values, so removing the corresponding `if x != ""` guards in
  production code wouldn't have failed any of these tests. Fixed with
  two new targeted tests (`TestLoadRuntimeConfig_
  EmptyNameKeepsCfgDefault`, `TestLoadState_
  CurrencyFallsBackToCfgDefaultWhenUnset`) that explicitly delete the
  seeded row via `data.NewSettingsRepo(db).Delete(...)` before
  asserting — both independently mutation-probed by the author after
  the fix and confirmed to fail against the reverted guard.
- **F3 (medium → real bug)** — the `LoadState` zero-value discard bug
  above; fixed, TDD-verified.
- **F4 (low)** — `TestBuildMenu`'s "doesn't mutate the caller's slice"
  assertion couldn't fail: a same-length composite literal always has
  `len == cap`, so `append` reallocates regardless of whether
  `BuildMenu` copies first. Fixed with a dedicated test giving the
  base slice real spare capacity and a sentinel value at the
  overwrite-risk index; mutation-probed (`items := base` instead of a
  copy) and confirmed to fail.
- **F5 (doc)** — noted here: after this fix, `UT_TAX_INCLUSIVE` becomes
  DB-pinned from the first boot post-upgrade onward (the boot-time
  Load→Save pattern materializes the row), same as `UT_CURRENCY`/
  `UT_STORE_NAME`/`UT_TAX_RATE` already behave. Not a behavior change
  in practice (those fields already worked this way; `tax_inclusive`
  now joins them) and doesn't change any documented env-var contract
  in `README.md`, so no README edit — recorded here per CLAUDE.md's
  behaviour-change rule.
- **F6 (process)** — this review record.
- **F7 (nice-to-have)** — logged the orphaned `pos.tax_inclusive` seed
  row cleanup as a `QUEUE.md` follow-up (a future append-only migration
  `DELETE FROM settings WHERE key='pos.tax_inclusive'`).
- **F8 (nice-to-have)** — added `TestSaveRuntimeConfig_
  WritesExpectedKeys`, pinning the literal key names `SaveRuntimeConfig`
  writes (can't reference `pages/common`'s `Key*` constants directly —
  `common` imports `settings`, so that would be a cycle) so a future
  key-name drift between the two packages fails loudly rather than the
  way `pos.tax_inclusive`/`store.tax_inclusive` silently diverged.

The reviewer also confirmed as genuine (its own independent mutation
probes, not the author's): `TestDeps_StateMuSerializesConcurrentAccess`
(removing the mutex produces a real `-race` failure),
`TestSaveState_RoundTripsThroughLoadState` (swapping two field writes
in `SaveState` fails with a clear diff), the repository-pattern
compliance of the diff (zero raw SQL; `data.NewSettingsRepo(db).Delete`
in tests is legitimate use of a pre-existing repo method), and the
absence of real client/shop names or secret-shaped literals.

**Accepted remainder, not pursued**: `internal/settings`'s uncovered
~15.4% is `SaveRuntimeConfig`'s six near-identical
`if err := s.Set(...); err != nil { return err }` branches past the
first `Set` call. The reviewer confirmed a SQLite `CREATE TRIGGER …
RAISE(ABORT)` *could* target any single branch and would pass CI (the
data-access guard exempts `_test.go`), but rejected it as coverage
theater — all six branches are byte-identical with no distinct
behavior to verify. The reviewer's own suggested alternative (whether
`SaveRuntimeConfig` should be transactional, so a mid-way failure
doesn't leave keys 1..n-1 written and n..7 not) is a real but
low-impact gap given the idempotent boot-time Load→Save usage —
logged as a `QUEUE.md` follow-up rather than expanded here.

## Verified beyond automated tests

- `go test ./internal/pages/...` (the real production caller of
  `LoadState`/`SaveState`, `internal/pages/init.go`,
  `settings_page.go`, `setup_page.go`) re-run clean after the
  `state.go` fix, under `-race`.
- 8 mutation probes total across both packages (author: currency_symbol
  guard, tax_inclusive key, name guard, currency fallback, BuildMenu
  aliasing; reviewer: name-guard drop, currency-default drop, mutex
  removal, SaveState field swap) — every one caught a real assertion
  failure, none passed against broken code.
- Full `go build ./...`, `go vet ./...`, both CI guards
  (`guard-data-access.sh`, `guard-i18n.sh`) green.
