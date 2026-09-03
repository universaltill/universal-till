# Review: dispatch `fiscal.sign.ask` on refund/return completion (ut-docs#1405)

**Date:** 2026-09-03
**Card:** ut-docs#1405 (rebuild of `universal-till` PR #594, closed unmerged 2026-09-02)
**Author:** Farshid Mirza (pipeline, `lane:cloud-54`)
**Reviewer:** independent Opus subagent, worktree-isolated, different model from the implementer (Sonnet)

## What shipped

The two refund/return completion paths previously completed with **no
signing attempt at all** — not "signing failed", just never dispatched —
even though the ADR-0048 hard gate already treats a refund/return as
fiscally relevant as a sale. Both now dispatch `fiscal.sign.ask` (ADR-0044
Decision 1) immediately before `pos.CompleteSale` and declare/record the
result after it, mirroring `internal/pages/pos_api.go`'s `completeTender`
exactly:

- `internal/pages/refund_page.go` — `POST /api/refund`
- `internal/pages/inventory_api.go` — `CreateReturn` (`POST /api/inventory/return`)

This is a fresh re-implementation on current `main`, not a rebase/merge of
the original draft (`fix/999-fiscal-sign-ask-refund-return-dispatch`,
PR #594) — that branch predates the 2026-08-31 `main` history-rewrite
incident (ut-docs#1374/#1378) and is ~1300 commits / 204k lines diverged,
per #1405's own requirement. PR #594's one real commit (`aed80ea3`) was
used as design reference only (it was already reviewed clean except for
the one blocker below, which is now resolved) — the diff here reproduces
its shape from scratch against the current tree.

**The blocker that closed PR #594** — the `fiscal.sign.ask` payload
carried no way to distinguish a refund from a sale of the same amount, so
a DSFinV-K signer would sign a refund as positive turnover — is resolved
independently of this diff: `ut-docs#1203`/`universal-till` PR #720
(merged 2026-09-02, contract 1.6.0) added `sale_type` to
`buildFiscalSignPayload`, and both call sites already set
`SaleType: "return"` on their `SaleInput`/`ReturnInput` (true before this
diff). This diff wires the dispatch call itself; new test coverage below
asserts the signer-observed payload actually carries `sale_type: "return"`.

Adds 6 tests (2 pre-existing scenarios × 2 call sites, plus 1 new
regression — see below): approved → dispatched exactly once, `sale_type`
correct, no marker; unreachable → completes anyway, journaled unsigned,
nothing queued for retry (ADR-0056).

## Independent review

Spawned a fresh, worktree-isolated **Opus** subagent (this card is
`complexity:medium`, built at Sonnet — model routing puts review one tier
up) with no access to the implementer's reasoning, briefed on the
`completeTender` reference pattern and told to actually run build/vet/
tests/guards and independently re-verify the TDD claim (revert-then-
restore), not just read the diff.

### Findings — all fixed before merge

1. **BLOCKER — rounding mismatch, `inventory_api.go`'s `CreateReturn`
   (pre-existing, newly load-bearing).** `CreateReturn` computed its own
   return total with a **truncating** int64 conversion + integer tax
   division, while `pos.CompleteSale` recomputes the total with **half-up**
   rounding (`pos.ComputeTaxBasisPoints`). On an entirely ordinary price
   (e.g. 99p @ 20% VAT: 99×0.2=19.8, truncated=19, half-up=20) the two
   disagree by one minor unit and `CompleteSale` rejects with `payments do
   not cover total`. This mismatch predates this diff, but this diff
   changes its consequence: dispatch now fires **before** `CompleteSale`,
   so a signer would be asked to sign a return that then never persists —
   an orphan, irreversible TSE record for a return that never happened,
   exactly the harm ADR-0044 D1's ordering exists to prevent. Reviewer
   confirmed this empirically (a throwaway probe: `sale_type` dispatched
   once, zero return rows persisted) before the fix, and that the sibling
   `refund_page.go` path is clean (`computeRefundTotal` already uses
   `pos.ComputeTaxBasisPoints`, the same math `CompleteSale` uses).
   **Fixed:** `CreateReturn`'s `returnTotal` now sums
   `pos.AmountForQuantity` + `pos.ComputeTaxBasisPoints` per line — the
   same primitives `computeSaleTotals` itself calls. (This return carries
   no `SaleDiscount`/`ServiceCharge`/vouchers, so summing per-line results
   is algebraically identical to `computeSaleTotals`'s own
   `VATBandsForSale` apportionment here — that function's discount
   redistribution is a no-op at `discountTotal == 0`.) Added
   `TestReturnFiscalSignAsk_RoundingMatchesCompleteSale`, a regression
   fixture on exactly the 99p/20% case: the return must complete (not
   reject), sign exactly once, and leave exactly one persisted return row
   — not a signed-but-orphaned zero.
2. **BLOCKER (repo policy) — `ut-docs/reference/contracts/fiscal-sign-ask.md`
   left stale.** Two places contradicted the new dispatch surfaces: the
   registration table's "Phase" claimed "fires once per sale" (ambiguous —
   now three call sites, including two completion paths that aren't the
   tender); the "Known-offline short-circuit" section claimed "both till
   surfaces carry the signal", which is no longer true for either new
   path. **Fixed:** updated both sections in the same session — the Phase
   row now names all three dispatch call sites explicitly, and the
   offline-short-circuit section documents the real gap (below) instead of
   asserting something false.
3. Non-blocking — dangling doc reference: a code comment in
   `refund_page.go` pointed at
   `docs/code-reviews/2026-08-28-fiscal-sign-refund-return-dispatch.md`,
   which does not exist (that record lived only on PR #594's closed,
   diverged branch). **Fixed:** now points at this record instead.
4. Non-blocking — `newInventoryReturnTestDeps` duplicates most of
   `newRefundTestDeps`. **Deferred** — matches the existing pattern PR
   #594 already established and review-approved; a shared helper is a
   genuine but separable cleanup, not part of this card's scope.
5. Non-blocking — `t.Setenv("UT_AUTH", "off")` in the inventory-return
   test file is inert (`getSessionUserID` never returns `""` for
   `CreateReturn`, so the auth guard never trips either way). **Left as
   documentation of the endpoint's actual auth posture** — harmless, and
   removing it doesn't change test behavior.

### Findings — deferred as follow-ups (documented, not fixed here)

- **No offline short-circuit on either new dispatch path** (`refund_page.go`,
  `inventory_api.go`): neither `SaleInput` sets `Offline`, so a known-
  offline till still burns the full 3s `fiscalSignAskBudget` on a refund/
  return, and the resulting declaration reads as a generic backend timeout
  rather than the honest offline reason. Needs a form/UI change (neither
  POST body carries an offline signal today) — out of scope for this
  card, now documented as a known gap in the contract itself rather than
  silently contradicting it. Filed as a follow-up Backlog card.
- **`inventory_api.go`'s `CreateReturn` hardcodes `Currency: "GBP"`** and
  leaves `TaxInclusive` false — pre-existing, and internally consistent
  (the same values reach both the signer payload and `CompleteSale`, so
  nothing here is newly wrong), but a German shop's TSE signer now
  receives `"currency":"GBP"`/`"tax_inclusive":false` for every return
  through this specific route. Pre-existing defect, newly load-bearing
  now that a real signer is actually dispatched here. Filed as a
  follow-up Backlog card.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on all 4 changed files:
  clean.
- `go test ./internal/pages/...` (full package, no filter): green,
  ~58s.
- `go test ./internal/pages/ -race -run FiscalSignAsk`: green, no races
  (reviewer's isolated worktree run).
- Guards: `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`
  all pass — no inline SQL added outside `internal/data`, no self-order/
  `Engine` interaction (not applicable, no kiosk route touched), no new
  hardcoded user-facing strings (no UI surface changed at all: no
  templates, no `web/`, no new page route).
- **TDD re-verification (independent, not the implementer's own claim):**
  reviewer stripped the `dispatchFiscalSignAsk` call + declare/record
  block from `refund_page.go` (reverting it to pre-diff behavior),
  rebuilt, and reran the two new refund tests — both failed with the
  expected errors (`fiscal.sign.ask must be dispatched exactly once ...
  got 0 invocations`; missing `unsigned_fiscal_signing` audit row).
  Restored the file; both passed again. Additionally mutation-tested the
  `sale_type` assertion by forcing the payload's `sale_type` to `"sale"`
  regardless of the real value — both approved tests failed on that
  assertion alone. The new tests are not vacuous.
- No real client/shop name anywhere in the diff (fixtures reuse the
  existing `"Apple"`/`"ABC"`/`GBP` test data); no literal credential/
  secret.
- No UI surface changed (no templates, `web/`, or new route) — the UX-
  guidelines and help-manual review steps don't apply to this diff.

## Verdict

**Safe to merge.** The dispatch wiring is a faithful, correct mirror of
`completeTender` — right ordering (after any payment-provider interaction,
before `CompleteSale`), right pointer semantics (`in.SaleID` minting
reaches the persisted row), right outcome gating (declare on failure/
offline-skip, record evidence on approval, nothing on no-signer/no-
opinion) — confirmed by an independent, worktree-isolated review that
found and got fixed one real compliance-relevant defect (the rounding
mismatch) before it could ship. Both new follow-up items (offline signal,
GBP hardcoding) are filed as separate Backlog cards rather than silently
dropped.

## Deferred / follow-up cards filed

- Refund/return dispatch paths don't carry the till's offline signal
  (`fiscal.sign.ask` known-offline short-circuit never fires on either
  path) — needs a form/UI change to thread `offline` through.
- `inventory_api.go`'s `CreateReturn` hardcodes `Currency: "GBP"` /
  `TaxInclusive: false` — wrong for a German (EUR, inclusive-priced) shop
  once a real TSE signer is listening on this route.
