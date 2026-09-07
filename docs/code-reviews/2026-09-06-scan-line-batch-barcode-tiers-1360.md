# Code review: batch ResolveScanLine's barcode-tier lookups (ut-docs#1360)

**Date:** 2026-09-06
**Author (Dev):** Sonnet, inline (`complexity:medium`)
**Reviewer:** Opus, independent subagent, isolated worktree
**PR:** universaltill/universal-till (this branch, `fix/1360-scan-line-batch-barcode-tiers`)

## What shipped

`internal/data/pos_repo.go`'s `ResolveScanLine` (the ADR-0059 §3 scan-resolution
entry point) used to probe up to four barcode-lookup tiers sequentially, one
round trip at a time: variant-by-LookupKey, item-by-LookupKey, and — only
when the raw scanned code differs from the decoded LookupKey (the two
embedded-weight/price symbologies) — variant-by-rawCode, item-by-rawCode.

This batches all four tiers into a single SQL query
(`resolveScanBarcodeTiers`) using `UNION ALL` with an explicit `tier`
ordinal column, `ORDER BY tier LIMIT 1` — the database picks the winning
tier in one round trip instead of the Go code probing tier by tier. The
raw-code tiers (3, 4) are omitted from the query entirely (not just
short-circuited) when the raw code equals the LookupKey, exactly mirroring
the original `dec.LookupKey != code` guard.

The old `resolveVariant`/`resolveItem` helpers are removed (fully dead
after the rewire — no other call sites). Price resolution
(`resolvePrice`/`ResolveCurrentPrice`) is **unchanged and deliberately out
of scope** — the card's own acceptance criteria are scoped to "the barcode
fallback tiers"; caching the enabled-symbologies settings read is a
separate, already-filed card (ut-docs#1361).

## Round-trip counts (measured, not estimated)

| shape | before | after |
|---|---|---|
| direct item-barcode hit | 4 (1 wasted variant-tier probe + 1 item hit + 2 price) | 3 (1 barcode-tier query + 2 price) |
| direct variant-barcode hit | 3 (already optimal — no wasted probe) | 3 (unchanged) |
| raw-code fallback hit (ut-docs#934 shape) | 6 (4 barcode-tier probes + 2 price) | 3 (1 barcode-tier query + 2 price) |
| no match | 2 | 1 |

Price resolution costs 2 round trips (a `price_history` miss, then a
base-price fallback) for any item with no scheduled future price change —
the ordinary case, and unaffected by this change either way.

Regression test: `internal/data/pos_repo_scanline_querycount_test.go`,
`TestResolveScanLine_QueryCount` — a second connection to the same
on-disk DB through a counting `driver.Connector` (reusing
`export_repo_querycount_test.go`'s existing harness), asserting the exact
SELECT count for each shape above.

## Independent review — findings and disposition

**Verdict: SAFE TO MERGE.** No blocking findings. The review re-verified
TDD by reverting *only* the production code (keeping the new test) and
confirming it failed with the documented "before" counts (4 / 3 / 6 / 2),
then restoring and confirming it passed again. It also wrote 10 adversarial
probes checked against both the old and new implementation (tier
precedence, active/inactive filtering on both items and variants, variant
name suffixing, the price-resolution axis, the price-history-override axis,
zero-`Decoded` on raw-code-tier matches) with identical results on both.

Non-blocking findings, all fixed before merge:

1. **Coverage gap on the money/weight-sensitive `viaLookupKey` bit**
   (blocking-adjacent — fixed). Mutating `resolveScanBarcodeTiers`'s
   `tier <= 2` to always-`true` passed the whole suite: nothing pinned
   that a raw-code-tier (3/4) match must report a zero `barcode.Decoded{}`
   — the property the pre-batching code guaranteed structurally (two
   literal `barcode.Decoded{}` returns physically inside the
   `dec.LookupKey != code` block) but this refactor turned into a value
   computed from SQL-supplied tier ordinals. Fixed: the raw-code-fallback
   subtest now asserts `dec == barcode.Decoded{}` directly, so a future
   tier-renumbering bug that lets an embedded weight/price leak onto a
   raw-code-tier match — silently wrong quantity/money on a receipt — now
   fails a test.
2. **Comment overstated the miss-case saving** (`pos_repo.go`, the
   `ResolveScanLine` doc comment) — said "a full miss costs one instead of
   four" without qualifying that four is the *worst* case (raw code ≠
   LookupKey); the common case (every plain symbology) was already two.
   Reworded to state both.
3. **Internal inconsistency in the test's own doc comment** — said "plus
   one price-resolution round trip" while the same file correctly says two
   a few lines later. Reworded to match.
4. **`requireCounted` guard called strictly redundant here** (not fixed —
   kept deliberately). The reviewer noted all four count assertions are
   exact equality, so a silently-broken counting harness (reporting 0)
   would already fail them without the explicit guard — unlike
   `TestSalesForExport_ConstantQueryCount`'s `small != large` check, which
   really would be vacuous at 0==0. Keeping it anyway: it gives a clearer
   failure message naming the actual problem (harness broke) rather than a
   bare count mismatch, and costs nothing.
5. **Pre-existing 2-4× amplifier one layer up** (not fixed — filed as
   ut-docs#1660, `complexity:easy`, Backlog). `internal/ui/buttons.go`'s
   `PriceResolverAdapter.Resolve` calls the full `ResolveShortcutLineDecoded`
   chain up to four times per call, discarding wrong-shape results, instead
   of resolving once and switching on the shape. Out of scope for this
   card (a different file, a different kind of batching), but it multiplies
   whatever this card saves at the repo layer on the first scan of any new
   code (mitigated afterward by `cacheScan`).
6. **No review record yet** — this file; existed only because the reviewer
   ran against the WIP pre-merge snapshot, as expected.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `go test ./...`
  (repo-wide, all green) — run by both Dev and the independent reviewer.
- `golangci-lint run ./...` — 0 issues repo-wide, confirming the `unused`
  check (`.golangci.yml`) is satisfied now that `resolveVariant`/
  `resolveItem` are fully removed, not just superseded.
- `bash scripts/ci/guard-data-access.sh` — all SQL stays inside
  `internal/data`.
- ADR-0059 §6 / ut-docs#958's documented tier-ordering asymmetry
  (`TestScanDeleteExists_CollisionResolutionIsDeliberatelyAsymmetric`) —
  re-verified to still resolve the same winner under the batched query.
- SQLite UNION column-shape safety: `item_barcodes.barcode` and
  `variant_barcodes.barcode` are both `TEXT PRIMARY KEY`
  (`internal/db/migrations/001_init.sql`), so each UNION branch yields at
  most one row — `ORDER BY tier LIMIT 1` is fully deterministic, no
  tie-break ambiguity from dropping the per-branch `LIMIT 1`.
- `internal/pages`'s `TestScanAPI_WeightEmbeddedLabel` (the end-to-end,
  HTTP-layer scale-label test referenced in `ResolveScanLine`'s own
  comments) — still passes, confirming the embedded-weight decode reaches
  the API layer correctly through the batched path.
- No real client/shop name used anywhere in the diff; no secret-shaped
  literals.

## Deferred / explicitly out of scope

- ut-docs#1361 (caching the enabled-symbologies settings read) — separate
  card, untouched here.
- ut-docs#1660 (the `PriceResolverAdapter.Resolve` call-count amplifier) —
  filed from this review's finding #5 above.

## Safe-to-merge verdict

**Yes.** Behavior-preserving (independently re-verified via TDD revert and
10 adversarial probes), the round-trip reduction is real and measured, all
CI-relevant gates pass, and the one money/weight-sensitive coverage gap the
review found is now closed by a direct pin.
