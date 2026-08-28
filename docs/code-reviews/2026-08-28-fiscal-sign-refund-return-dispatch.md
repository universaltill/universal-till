# Code review — dispatch `fiscal.sign.ask` on refund/return completion (ut-docs#999)

- **Date:** 2026-08-28
- **Branch:** `fix/999-fiscal-sign-ask-refund-return-dispatch`
- **Reviewer:** independent reviewer (different model from the implementer)
- **Refs:** ADR-0044 (swappable fiscal signing provider), ADR-0048 (German TSE
  hard gate), ADR-0056 (no re-sign / no retry queue), ADR-0041 (generic plugin
  extension points), contract `ut-docs/reference/contracts/fiscal-sign-ask.md`
- **Verdict: DO NOT MERGE AS-IS — one blocking finding (B1).** The mechanical
  change is correct and faithful to the reference pattern; the problem is what
  the now-dispatched payload *says*.

---

## What shipped

Two production call sites that previously completed a fiscally relevant
transaction with **no signing attempt at all** (not "signing failed" — never
dispatched) now dispatch `fiscal.sign.ask`:

- `internal/pages/refund_page.go` — the `POST /api/refund` handler
- `internal/pages/inventory_api.go` — `CreateReturn`, `POST /api/inventory/return`

Each gets a `dispatchFiscalSignAsk(...)` call immediately before its
`pos.CompleteSale(...)`, and a `declareUnsignedFiscalSale` /
`recordFiscalTSEEvidence` pair after it, gated on the dispatch result —
mirroring `internal/pages/pos_api.go`'s `completeTender`.

Plus two new test files (4 tests): `refund_fiscal_sign_test.go`,
`inventory_return_fiscal_sign_test.go`.

No `web/` changes, no new routes, no new user-facing strings, no file writes,
no migrations.

---

## Findings

### B1 — BLOCKING: the ask payload cannot express that a refund is a refund

`fiscalSignAskPayload` (`internal/pages/fiscal_sign_hook.go:53`) carries no
`sale_type`, no direction flag, and no negative amount. `buildFiscalSignPayload`
derives every figure from `Lines`/`SaleDiscount`/`ServiceCharge` — all positive
on a refund — and additionally clamps a negative `total` to zero
(`fiscal_sign_hook.go:386`). The contract states outright: *"No fields beyond
these are sent."*

Before this change `completeTender` was the only dispatcher, and both of its
callers (`pos_api.go:994`, `self_order_shop.go:340`) build `SaleType: "sale"`.
So a non-sale transaction has **never** been sent through this contract. This
change is the first, which makes the gap newly reachable rather than
pre-existing.

**Verified empirically**, not just by reading. I captured the real payload a
€2.40 refund dispatches (scratch test, since removed):

```json
{"sale_id":"2f078777-330e-45db-926c-7bb0f7fbb958","currency":"GBP","total":240,
 "tendered_at":"2026-08-28T01:56:18Z",
 "payments":[{"method":"cash","amount":240,"tip_amount":0}],
 "vat_breakdown":[{"rate_bp":2000,"net":200,"tax":40}],"tax_inclusive":false}
```

Every monetary figure is positive. This is byte-for-byte the shape a €2.40
**sale** of the same items produces. A signer has no way to tell them apart.

**Consequence.** For a German shop with a signing plugin installed, a
DSFinV-K-compliant signer would sign each refund as a positive-turnover
Kassenbeleg rather than a Rückgabe — an irreversible TSE record asserting a
sale that never happened, inflating declared turnover in the fiscal journal.
ADR-0056 means core never re-asks, so it can never be corrected.

**Why this is worse than the status quo, not a partial improvement.** Today a
refund is unsigned but *honestly declared*: `unsigned_fiscal_signing` audit
marker, receipt outage notice, operator Problem. An auditor sees a documented
gap. After this change the gap is replaced by a silent, wrong, permanent
record. Under §146a AO an incorrect TSE entry is materially worse than a
declared missing one.

Note the codebase's own convention agrees: the sibling plugin-facing
`plugins.SaleCompletedEvent` (`internal/plugins/ipc.go:124`) *does* carry
`SaleType string \`json:"sale_type"\` // "sale" | "return"`. The signing
payload is the outlier.

**Not fixed here — deliberately.** The fix is a versioned cross-repo plugin
contract change (add direction to `fiscalSignAskPayload`, bump the contract to
1.6.0, amend `ut-docs/reference/contracts/fiscal-sign-ask.md`, and coordinate
`ut-plugin-tax-de` / `ut-plugin-tax-fiskaly`), with a compliance dimension and
probably an ADR note. That is a design decision for the architect/product
owner, not a reviewer's unilateral edit.

**Recommended shape:** land the contract's direction field first, then this
dispatch. The ADR-0048 hard gate already treats a refund as fiscally relevant,
so the ordering matters only for correctness of the signed record, not for
whether refunds are gated.

### N1 — non-blocking: `SaleInput.Offline` is never set on either path

`dispatchFiscalSignAsk`'s known-offline short-circuit reads `in.Offline`
(`fiscal_sign_hook.go:270`). Neither the refund nor the return `SaleInput`
literal sets it — neither handler has an offline signal threaded from its
request, unlike the tender DTO. So an offline till doing a refund spends the
full 3s ask budget on a call it already knows cannot succeed, then journals the
gap as a backend outage rather than "known-offline".

Not a correctness bug and not an offline-first violation: the refund still
completes and the budget is bounded. Worth a follow-up ticket once B1 is
settled, since it affects the same payload plumbing.

### N2 — non-blocking: avoidable test-fixture duplication

`inventory_return_fiscal_sign_test.go` adds `newInventoryReturnTestDeps`, which
re-implements the existing `newInventoryAPITestDeps`
(`inventory_api_test.go:19`) nearly verbatim, and `postInventoryReturn`, which
re-implements `postInvJSON` (`inventory_api_test.go:64`). It also seeds via
`seedCompletedSaleForRefund` with a hardcoded `"line-refund-1"` line id instead
of `seedCompletedSaleForReturn`, which returns the id.

The established pattern two files over is `newInventoryFiscalTestDeps`
(`inventory_return_fiscal_gate_test.go:24`), which *wraps* the shared helper
and layers on what it needs. Left unchanged deliberately: cosmetic churn on a
PR that is blocked pending a design decision adds noise. Worth folding in
whenever B1 is addressed.

## Checked and found clean

- **Ordering vs. the payment provider.** The load-bearing concern. In
  `refund_page.go` the blocking `payment.<key>.refund` provider gate
  (`refund_page.go:298`) runs and returns on decline **before** the dispatch —
  so no TSE record is produced for a refund the provider then refuses. Matches
  `completeTender`'s "after authorize, before CompleteSale" ordering exactly.
  `CreateReturn` has no provider interaction (fixed `"cash"` method), so the
  ordering question does not arise there; the comment says so.
- **Correct variables in each scope.** `refund_page.go` uses `r.Context()`,
  `d`, `repo`, `actorID`, `saleID`; `inventory_api.go` uses `ctx` (`=
  r.Context()`), `dp`, `repo` (`= data.NewPOSRepo(dp.Db)`), `actorID` (session),
  `returnSaleID`. No cross-wiring, no background context.
- **Dispatch is against the final, post-mutation input.** Both pass a pointer
  (`&saleInput` / `&returnInput`) to the same local the subsequent
  `CompleteSale` receives, so the `SaleID` that `dispatchFiscalSignAsk` mints
  propagates to persistence (`internal/pos/sales.go:591` honours a pre-set
  `SaleID`) — the declare/evidence calls therefore key off the row that was
  actually written.
- **Failure declaration can never target a non-existent sale.** The
  `if err != nil { … return }` immediately after `CompleteSale` is intact and
  unmodified in both handlers, so the declare/evidence blocks are unreachable
  when the sale did not persist.
- **No missed third call site.** `SaleType: "return"` appears in exactly two
  production files (`refund_page.go:321`, `inventory_api.go:519`) — both
  covered — plus `tax_summary_test.go`. There is no `SaleType: "refund"`
  anywhere. `sync_sales.go:313` also calls `pos.CompleteSale`, but it is a
  replica→primary journal replay of an already-completed remote sale; signing
  belongs to the originating till and dispatching there would double-sign.
  Correctly excluded.
- **Residual pre-existing risk, mirrored not introduced:** dispatch precedes
  `CompleteSale`, so a `CompleteSale` failure leaves a signed-but-unpersisted
  sale. Inherent to ADR-0044's tender-phase ordering and identical in
  `completeTender`. Not a new defect.
- **Repository pattern:** `guard-data-access.sh` passes; no SQL added outside
  `internal/data` (the new SQL is in `_test.go` files, which the guard scopes out,
  consistent with the existing fiscal/gate tests).
- **i18n:** `guard-i18n.sh` passes; the diff adds no user-facing string (only
  `log.Printf` server-side text, inside helpers that already existed).
- **money.Money** used throughout; no raw `int64` arithmetic introduced.
- **Recurring bug classes:** not applicable — the diff writes no files, so no
  missing `os.MkdirAll` and no cwd-relative path in place of `paths.Data(...)`.
  Confirmed by grep over the diff for `os.Create|WriteFile|MkdirAll|OpenFile|
  filepath.Join`: no hits.
- **No UI surface:** confirmed — the diff touches only `internal/pages/*.go`,
  no `web/`, no route registration. Manual/UX gates correctly skipped.
- **No real client/shop name** in test data (`com.test.fiscal-sign-*`,
  `Apple`/`ABC`); **no secret-shaped literal** anywhere in the diff.

## Verification performed

All commands run in the reviewer's own worktree.

| Check | Result |
|---|---|
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `gofmt -l .` | empty |
| `go test ./internal/pages/... -run 'TestRefundFiscalSignAsk\|TestReturnFiscalSignAsk' -v` | 4/4 pass |
| `go test ./internal/pages/...` (full package) | pass — 89.5s / 0.4s / 4.1s |
| `go test -race` scoped to the 4 new tests | pass, 24.8s |
| `bash scripts/ci/guard-data-access.sh` | pass |
| `bash scripts/ci/guard-i18n.sh` | pass — 1299 keys resolve, all locales match |

### Independent re-verification of the TDD claim

I did not take the implementer's red-then-green claim on trust. I reverted
**only** the two production files to their pre-fix state
(`git show HEAD~1:internal/pages/refund_page.go > …`, same for
`inventory_api.go`), left the new tests in place, confirmed the package still
**compiles** (so the failures below are genuine assertion failures, not build
errors), and re-ran the four tests. All four failed, on-topic:

```
--- FAIL: TestReturnFiscalSignAsk_ApprovedHasNoMarker
    inventory_return_fiscal_sign_test.go:83: fiscal.sign.ask must be dispatched
      exactly once for the return, got 0 invocations
--- FAIL: TestReturnFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares
    inventory_return_fiscal_sign_test.go:109: expected a sale/unsigned_fiscal_signing
      audit row for the return: sql: no rows in result set
--- FAIL: TestRefundFiscalSignAsk_ApprovedHasNoMarker
    refund_fiscal_sign_test.go:48: fiscal.sign.ask must be dispatched exactly once
      for the refund, got 0 invocations
--- FAIL: TestRefundFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares
    refund_fiscal_sign_test.go:81: expected a sale/unsigned_fiscal_signing audit
      row for the refund: sql: no rows in result set
```

`got 0 invocations` is exactly the "never dispatched at all" signature the
ticket describes — the tests fail for the right reason. Restoring both files
(`git checkout HEAD -- …`) returned all four to green. The tests are genuine,
not false-passes.

The two "approved" tests are worth calling out as well-built: asserting only
the *absence* of an `unsigned_fiscal_signing` marker would pass identically if
dispatch were never wired up, so both also assert an exact invocation count.
That is the assertion that actually fails on the reverted code.

### Known pre-existing `-race` full-package hang — out of scope

A full-package `go test ./internal/pages/... -race` run hangs inside
`internal/db.migrate`, on a different randomly-selected test each run
(`TestPrintKitchen_…`, `TestTablesPageCreate_…`). I did not re-chase it; I did
sanity-check the claim that it is unrelated to this diff by running `-race`
**scoped to the four new tests**, which completes cleanly in 24.8s. Combined
with the non-race full-package suite passing, this diff is not implicated. It
is a pre-existing environment/test-isolation issue and belongs in its own
ticket.

---

## Verdict

**Do not merge as-is.** The implementation does exactly what ut-docs#999 asked
and mirrors `completeTender` faithfully — mechanically it is clean work, the
ordering against the payment-provider gate is right, and the tests are honest.
But faithfully mirroring the sale path is precisely what surfaces B1: the same
payload builder is now fed a semantically different transaction it has no
vocabulary to describe, and the result is a permanent, irreversible fiscal
record that says "sale" about a refund.

Sequence the contract change first (direction field, contract 1.6.0, doc
amendment, signer-plugin coordination), then land this dispatch on top. N1 and
N2 can ride along with that follow-up.
