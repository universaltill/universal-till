# 2026-11-20 — refund: a client disconnect after the provider refunded no longer loses the return (ut-docs#6784)

Lane `lane:cloud-41`. Card sized easy, bumped to medium after review (the
widest window was the provider hook itself).

## What shipped
- `internal/pages/refund_page.go`:
  - The `payment.<key>.refund` dispatch runs on
    `context.WithoutCancel(r.Context())`. A WASM guest is bound to its ctx
    (`wasm_runtime.go:858` `WithTimeout`, `WithCloseOnContextDone`), so a
    closed tab used to kill a provider call that may already have refunded.
    The till then answered 402 "declined" and recorded nothing. The call is
    still bounded by the plugin call timeout (`paymentGateTimeout` floor for
    refund events, `wasm_runtime.go:178/199`). This also keeps the plugin's
    dispatch audit row (`plugins/ipc.go`).
  - `recordCtx`: when a hook really moved money (`refundDelivered`), all the
    recording runs detached. Cash or hookless refunds stay cancellable until
    `pos.CompleteSale` commits (ADR-0146 D1: the cashier hands cash over only
    after success). After the commit, everything runs detached for every
    method: leg rows, audits, TSE/receipt/device evidence, unsigned marker,
    stock publish, cloud-link frame, credit note.
- `internal/pages/refund_disconnect_test.go` (new):
  - `TestRefund_ClientDisconnectAfterProviderRefundStillRecordsReturn`: a
    two-leg card sale. The provider hook cancels the request ctx while it
    runs. The test asserts one return sale, its `sale_links` row and its
    `refund_leg_refunds` rows.
  - `TestRefund_ClientDisconnectCashRefundRecordsNothing`: a cash refund
    whose `fiscal.sign.ask` subscriber cancels the request mid-request. The
    test asserts no return row.
- `internal/pages/fiscal_sign_cannot_sign_test.go`: the shared fake refund
  handler returns `ctx.Err()` once its ctx is done, as a real guest does.

## Review
Independent review by Opus 5.5 in a fresh context. The author was Sonnet
5.5. Verdict: approve-with-fixes.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The refund hook itself still ran on `r.Context()`, the widest window | fixed (round 2) |
| 2 | medium | With no money moved, post-commit evidence, audit and credit-note writes were still cancellable | fixed: unconditional detach after commit |
| 3 | minor | The cash test cancelled before the request, so it pinned nothing (Tester confirmed: `|| true` mutation still passed) | fixed: cancel from a fiscal.sign.ask stub; `|| true` now fails |
| 4 | minor | The plugin dispatch audit was lost on a cancel during the hook | fixed by 1 |
| 5 | info, pre-existing | `CompleteSale` failing for another reason (DB busy) after the provider refunded still loses the record; the sale path has the same disconnect bug | deferred → ut-docs#6805 (sale path); DB-failure case noted there |

## Verification
- Round 1 Tester (Sonnet, not the Dev agent):
  - Mutations: (a) dropping the detach and (c) putting CompleteSale back on
    `r.Context()` both fail the delivered test.
  - `go test ./...`: 124 packages ok.
  - gofmt, vet, build and golangci-lint (internal/pages) are clean.
  - All 12 guards run exit 0, including guard-data-access, guard-fixed-dates,
    guard-i18n, guard-i18n-meta, guard-kiosk-engine, guard-core-neutral and
    guard-page-http-error.
- Round 2 (Dev): delivered test fails before the fix with 402 and no return
  row, and passes after. Cash test fails under the `refundDelivered || true`
  mutation. `-run 'Refund|ClientDisconnect|Payment|Tap|FiscalSign|CreditNote'`
  is ok, and lint reports 0 issues.
- Orchestrator re-verified the TDD claim personally. With `main`'s
  `refund_page.go`, `TestRefund_ClientDisconnectAfterProviderRefundStillRecordsReturn`
  fails ("return sales recorded = 0 … response 402"). With the fix, the
  `ClientDisconnect|Refund` tests pass.
- Not verified here: the full `internal/pages -race` run timed out locally
  (10m and 25m) on an unrelated `TestCSPReportOnlyMiddleware_*` while the
  machine was shared. No DATA RACE was reported. The
  `-race -run 'Refund|ClientDisconnect'` run is ok. CI runs the full gate.

Pre-flight: till user-visible behaviour n/a (no UI or copy change; a refund
that used to fail on disconnect now completes, and the help topic describes no
disconnect case); en.json n/a; ADR n/a (applies the existing WithoutCancel
rule of engine_config.go / no_sale_api.go, consistent with ADR-0146 D1);
reference docs n/a; follow-up → ut-docs#6805.
