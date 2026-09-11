# Code review: ut-docs#2069 — partial-return-then-over-limit test coverage

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2069 — "Inventory 'Process a return' (CreateReturn)
doesn't net against prior returns/refunds — can double-return the same sale line."
**PR:** universaltill/universal-till, branch `pipeline/2069-return-partial-overlimit-test`
**Reviewer:** independent Opus subagent, fresh context, isolated worktree
**Author:** Sonnet (this cycle, `lane:cloud-24`)

## Background — this card was already substantively fixed

While picking up ut-docs#2069, discovery (this cycle's BA step) found the
actual fix already shipped on `main` in commit `69b935f` (PR #1071, closing
ut-docs#2064): `CreateReturn` already nets a requested return quantity
against `repo.ReturnedQuantitiesByOriginalLine`, and a regression test
(`TestCreateReturn_SecondReturnAgainstSameLineIsCapped`) already guards the
"return the same line twice for its full quantity" case. `go test` on
`main` confirmed both pass before this branch touched anything.

Checking #2069's own acceptance criteria against that existing coverage
found one real gap: AC bullet 2, "a partial return followed by a request
for more than what's left is rejected," was not independently exercised —
only the full-quantity double-return case was. This PR closes that one
gap. It is test-only; no production code changes.

## What changed

`internal/pages/inventory_api_test.go`:
- `seedCompletedSaleWithQtyForReturn` — a multi-unit sibling of the
  existing `seedCompletedSaleForReturn` seed helper, so a test can return
  part of a line and still have remaining quantity to probe.
- `TestCreateReturn_PartialReturnThenOverLimitRejected` — sells 3, returns
  1 (succeeds), requests 3 more against the 2 actually remaining (must be
  rejected, 400), then requests exactly the 2 remaining (must succeed).

## Review findings

**Verdict: approve as-is. No blockers.**

- Re-derived the claim from the handler (`inventory_api.go`'s
  `remaining := origLine.Qty - alreadyReturned[...]` computation and its
  400 rejection path), not from the commit message, and confirmed the new
  test's partial-then-over-limit case was genuinely uncovered by the
  existing suite.
- **TDD red→green personally re-verified, with two mutations, both
  reverted atomically in the same shell invocation:**
  - Removing the netting entirely (`remaining := origLine.Qty`)
    reproduces the exact real #2069 symptom: both the existing capped
    test and the new test return 200 where 400 is required.
  - An *over-correction* mutation (any prior return zeroes remaining)
    left the **existing** capped test passing while **failing** the new
    test's final assertion ("exactly the 2 remaining must still be
    accepted") — proof the new test is additive, not a restatement of
    existing coverage.
  - `internal/pages/inventory_api.go` confirmed byte-identical to `HEAD`
    after both mutations were reverted.
- Seed helper checked against the schema and runtime readers, not just
  copied by eye: its tax figures (300 net / 60 tax / 360 gross at 2000bp)
  keep the seeded sale on the tax-*exclusive* side of
  `pos.InferTaxInclusive`, consistent with its own line rows and
  `CreateReturn`'s own `returnTotal` recomputation; its reuse of the
  `itm1` seed id satisfies the live `sale_lines.item_id` FK
  `openPagesTestDB` enforces (`PRAGMA foreign_keys = ON`). Test isolation
  confirmed: fresh migrated SQLite DB per test in `t.TempDir()`, so the
  helper's hardcoded ids cannot leak between tests.
- **Cross-endpoint netting claim (AC bullet 3) verified by tracing both
  write and read sides**, not accepted on inspection alone: `sale_links`
  has a single writer, `pos.CompleteSale` (`internal/pos/sales.go:1130`),
  that both `/refund` (`refund_page.go:872`) and `/api/inventory/return`
  (`inventory_api.go:655`) route through, both setting per-line
  `RefundOfLineID`; `ReturnedQuantitiesByOriginalLine`
  (`data/pos_repo.go:3893`) reads that shared shape without distinguishing
  origin. The decision not to add a heavier HTTP-level cross-endpoint test
  (would require wiring both routers into one test fixture) was reviewed
  and accepted as a proportionate scope call on that structural basis.
- **One suspected latent defect probed and cleared**: `CreateReturn` uses
  a strict `>` against remaining while `/refund` allows a `1e-9` epsilon —
  looked capable of letting the line picker offer a fractional
  (weighed-goods) quantity the POST would then refuse. A throwaway
  end-to-end probe (sell 3, return 0.1 then 0.2, request 2.7) returned
  `remaining: 2.7` and accepted the 2.7 POST — SQLite's compensated
  `SUM()` doesn't accumulate the feared float drift. No defect; recorded
  here so it isn't re-investigated.
- **Deferred, accepted, no follow-up card**: cosmetic duplication of the
  unit-price literal (`100`) between the helper's `total := 100 * qty` and
  the `sale_lines` INSERT; `qty int` rather than the domain's `float64`
  (not needed for this case); the unused `receiptNo` return value (kept
  for signature parity with the sibling helper).
- **Optional, non-blocking, not filed as a card**: an HTTP-level test
  asserting `/refund` and `/api/inventory/return` genuinely share one
  returnable pool end-to-end — low incremental value given the shared
  write path already traced above.

## Verified beyond automated tests

- Full gate re-run on the branch: `gofmt -l .` clean, `go build ./...` OK,
  full `go test ./...` (all packages) 0 failures, `golangci-lint run
  ./...` 0 issues, all 26 runnable CI `build`-job guards exit 0.
  `guard-shellcheck-version.sh` could not run in the review container (no
  `shellcheck` binary present) — an environment gap, immaterial to a
  Go-test-only diff; unaffected on the real `ubuntu-latest` CI runner.
- No UI surface touched, no behaviour change to production code, so no
  `web/help/` topic update, screenshot regeneration, locale key, or ADR
  implication is owed.
- Commit author `Farshid Mirza <4035824+farshidmirza@users.noreply.github.com>`
  (GitHub noreply address, not an AI identity), with `Co-Authored-By:
  Claude Sonnet 5` — correct per ut-docs#247/#790.

## Disposition

No fixes required — approved as-is. Merged with `merge_method: "merge"`
(never squash/rebase, per this ecosystem's standing rule, ut-docs#250).
