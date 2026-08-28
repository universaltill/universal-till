# Code review: historical tax_total doc note (ut-docs#1114)

**Date:** 2026-08-28
**Card:** ut-docs#1114
**Complexity:** easy — build: inline (Sonnet), review: fresh-context Sonnet subagent

## What shipped

ut-docs#1114 asked for a product-owner decision on what to do about
historical `sales` rows whose `tax_total` disagrees with
`VATBandsForSale`'s re-derivation for one shape (inclusive pricing + a
whole-sale discount) — a gap ut-docs#1035's fix deliberately left
unmigrated. The product owner answered (2026-08-27, comment on the
ticket): no real shop is live yet, so:

1. **Reset/delete affected test sales data** on the live test tills (Task
   Runner, the imported Nima café backup). Verified this is not something
   this pipeline session can do: no repo-tracked seed/demo data reproduces
   the shape (`internal/data/seeddata/seeddata.go` seeds no `sales` rows at
   all; the only sales-touching scripts, `scripts/smoke-offline-sale` and
   `scripts/smoke_quickstart`, both use the wrong shape). The affected rows
   live only in the runtime SQLite DB on physical/cloud test devices this
   cold cloud session has no access to. This is an operator action on the
   actual device, out of this pipeline's reach — noted on the issue as the
   one remaining item, not silently dropped.
2. **A regression test for new sales going forward** — already covered by
   ut-docs#1035's own fix: `TestEODTaxBands_WholeSaleDiscountInclusiveThroughCompleteSale`
   (`internal/pages/eod_tax_bands_test.go`) exercises this exact shape
   through the real `CompleteSale` → `EndOfDay` path and asserts the
   `sum(band.Tax) == TaxNet` identity. Re-run here to confirm it still
   holds; no new test needed.
3. **A clear code comment/doc note** for the historical-data question, so
   it isn't silently forgotten once real shops exist — this PR.

Two comment-only additions, no functional change:
- `internal/pages/eod_tax_bands.go` — extends the existing design-notes
  block above `computeEODTaxBands` with the historical-row caveat, at the
  identity it breaks.
- `internal/pos/sales.go` — extends `computeSaleTotals`'s doc comment with
  the same caveat plus the GoBD-immutability reasoning against a silent
  migration, at the computation it describes.

## Independent review (fresh-context Sonnet subagent)

Reviewed cold, verified both factual claims in the new comment text
against the actual code (not taken on trust):

- `sum(band.Tax) == TaxNet` holds by construction for any sale persisted
  by the current build — traced `computeSaleTotals`'s `taxTotal` back to
  the same `VATBandsForSale`/`ApportionServiceChargeTax` calls a later
  re-derivation would use.
- Confirmed `TestEODTaxBands_WholeSaleDiscountInclusiveThroughCompleteSale`
  exists and exercises the exact shape through production code paths, not
  a shortcut.
- Independently re-verified (own grep, not the implementer's claim) that
  no repo-tracked seed/demo data reproduces the affected shape.
- Confirmed the GoBD/TSE-immutability premise is grounded in real code
  (`internal/fiscal/`), not invented, and echoes the prior human-reviewed
  ut-docs#1035 review doc rather than introducing a new unreviewed claim.
- Checked the new comment text against ADR-0040's forbidden-phrase list
  (`guard-compliance-claims.sh` doesn't scan `.go` files, so checked by
  hand too): describes what GoBD *requires* internally, never asserts a
  compliance outcome — no violation, and not user-facing text regardless.

**Verdict: PASS, no blocking findings.** One non-blocking observation (the
reviewer's snapshot raced the implementer's commit and briefly saw an
uncommitted tree) — confirmed resolved; the commit (`05c6413`) is present
and clean on the branch.

## Verification

| Check | Result |
|---|---|
| `gofmt -l internal/pages/eod_tax_bands.go internal/pos/sales.go` | empty |
| `go build ./...` | pass |
| `go vet ./internal/pos/... ./internal/pages/...` | pass |
| `go test ./internal/pos/...` | pass, 4.5s |
| `go test ./internal/pages/...` (full package) | pass, ~104s |
| `bash scripts/ci/guard-data-access.sh` | pass |
| `bash scripts/ci/guard-i18n.sh` | pass — 1301 keys resolve |
| `bash scripts/ci/guard-compliance-claims.sh` | pass — 221 files scanned |

No TDD claim to re-verify — comment-only change, no new test, no reverted
behaviour to confirm red-then-green against.

## What was verified beyond automated tests

- Grepped independently for any repo-tracked sales-shaped seed data that
  would need resetting per item 1 — none found, corroborating the
  implementer's scoping decision to treat item 1 as an operator action
  outside this session's reach rather than a code change.
- Confirmed the new comments don't contradict or duplicate the existing
  `eod_tax_bands.go` design-notes block or `computeSaleTotals`'s own doc
  comment — placed as additions, not rewrites.

## Verdict

Safe to merge. Comment-only, zero functional risk, both new claims
independently verified against the code rather than trusted from the
ticket. Item 1 (resetting live test-till data) remains open as an
operator action, recorded on the issue rather than silently dropped.
