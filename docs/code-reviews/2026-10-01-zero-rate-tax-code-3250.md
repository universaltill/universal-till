# Review: a 0% tax code is charged 0%, not the shop default (ut-docs#3250)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-24
- **Author:** Opus 5.5. **Reviewer:** Fable (independent, fresh context, separate worktree; `complexity:medium` per MODEL-ROUTING.md)
- **Card:** universaltill/ut-docs#3250

## Root cause

`effectiveTaxRateBPFor` (`internal/pos/service.go`) treated `TaxRateBP == 0`
as "unset" and fell back to the shop default. The resolve queries return
`COALESCE(t.rate_basis_points, 0)`, so an item with **no** tax code and an
item on a **0%** tax code reached the engine looking the same. Zero-rated
food (UK), exempt lines (PT) and any 0% code were charged the default VAT.
The demo catalogue's own `tax_zero` code was affected too.

## What shipped

- `internal/pos/service.go`: a line with a `TaxCodeID` is charged that
  code's `TaxRateBP` as-is, 0 included. A line with no tax code keeps the old
  chain (its own non-zero rate, then the shop default, then 2000).
- `internal/data/pos_repo.go`: the 8 resolve queries select `t.id` (the
  joined `tax_codes` row) instead of `i.tax_code_id`. An `items.tax_code_id`
  pointing at no row (possible in data written before foreign keys were
  really enforced, see `internal/db/db.go`) now reads as "no tax code" and
  keeps the default rate, rather than becoming a silent 0% from the
  `COALESCE`.
- Tests: `internal/pos/zero_rate_tax_code_test.go` (engine rate and basket
  totals, inclusive and exclusive); two resolve tests in
  `internal/data/pos_repo_resolve_test.go` (0% code keeps its id; dangling
  id resolves as no code).
- `CHANGELOG.md` "Fixed" entry with the behaviour change called out;
  `web/help/{en,de,tr,ar,fa}/tax-codes.md` step 5 says a `0` rate really is
  0% and only an item with no tax code gets the shop default.

Out of scope (by the card): the `tax.rate.ask` hook still can't answer an
explicit 0% (`internal/pages/tax_hook.go`, ADR-0134 / #2961). Found along
the way and filed: #3392, a shop **default** rate of 0% is charged as 20%
on items with no tax code (same shape, the other fallback).

## TDD evidence

Written first, run red on unchanged code:
`TestEffectiveLineTaxRateBP_ZeroRateTaxCodeChargesZero` (got 2000, want 0),
`TestBasketTotals_ZeroRateTaxCodeAddsNoTax` (inclusive: tax 0.42),
`TestResolveShortcutLine_DanglingTaxCodeIsNoTaxCode` (got "gone"). All pass
with the fix. The reviewer re-verified this independently by checking out
the old `service.go` / `pos_repo.go` in its own worktree.

## Driven run (beyond unit tests)

Real binary, throwaway data dir, demo catalogue: created a "Zero rated" tax
code through `POST /api/catalog/tax-codes`, put one demo item on it, scanned
it and a standard-rate item through `/api/pos/scan`, tendered cash through
`/api/pos/tender`. `sale_lines`: the 0% item stored `tax_rate_bp 0`,
`tax_amount 0`; the standard item `2000` / 19p. No visual surface changed;
no screenshots taken.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | CHANGELOG said "past sales are unchanged", but a sale parked before the update carries `tax_code_id` in its snapshot and is re-rated at 0% on resume. | Fixed: the entry says so. |
| 2 | nit | `TestResolveShortcutLine_ZeroRateTaxCodeKeepsItsID` also passes on the old code; its comment read as a reproduction. | Fixed: comment says it is a regression guard for the `t.id` change; the proof is in `internal/pos`. |
| 3 | nit | A parked sale snapshotted before this change for an item with a dangling tax code stores that id with rate 0 and resumes at 0%. | Accepted: needs pre-FK-enforcement data and a parked sale across the upgrade. |
| 4 | process | Review record missing. | This file. |

The reviewer checked every path that builds or rates a line: the single
`BasketLine` constructor (`internal/ui/buttons.go`), modifiers, kiosk
(`KioskEngine`, same resolver and asker), the line refresh in
`mergeResolved`, hold/resume, refunds, returns and LAN-synced sales (these
use the stored `sale_lines.tax_rate_bp`), VAT bands, service-charge
apportionment, EOD/invoice bands (none drop a 0% band), and every consumer
of `TaxCodeID` (tax hook payload, plugin overrides, diagnostics). None
mis-charge.

## Gate

`gofmt -l .` (no output), `go build ./...`, `go test` for every package (CI's
split: the main set plus `./internal/pages` and `./internal/plugins` with
`-timeout 20m`; 73 packages ok, 0 failures), `golangci-lint run` on the
touched packages (0 issues), and the guards `guard-data-access`,
`guard-help-drift`, `guard-help-topics`, `guard-i18n`, `guard-core-neutral`,
`guard-compliance-claims`, `guard-competitor-naming`, `guard-kiosk-engine`,
`guard-no-showmodal`, `guard-readme-local-links`,
`guard-migration-version-collision` all pass. No migration, no new locale
keys.

First CI run failed `guard-docs-shots`: the help sentence changed the
tax-codes topic hashes, and that guard was missing from the local run.
`make docs-shots` re-captured all 120 screenshots; every PNG came out
byte-identical, so only the four `tax-codes` topic hashes in
`web/help/img/manifest.json` changed. The guard now passes locally. The
whole build-job guard list was then run; all pass except
`guard-shellcheck-version`, which needs a shellcheck binary this container
lacks (no shell script is touched).

**Verdict:** safe to merge.
