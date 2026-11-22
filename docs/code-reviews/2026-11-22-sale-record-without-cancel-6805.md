# Review: a client disconnect can no longer drop a charged sale or its reversal (ut-docs#6805)

Date: 2026-11-22 · Branch `fix/6805-sale-record-without-cancel` · Lane `lane:cloud-41`
Author: Sonnet (Dev). Independent review: Opus 5.5, fresh context. Tester: Opus 5.5, separate agent.

## What shipped

`completeTender` (`internal/pages/pos_api.go`) runs on three contexts. All three keep the request's values (session, locale, paybroker binding):

- **`ctx`, the request context.** The pre-authorize gates and `dispatchFiscalSignStart` stay on it. A tender that moved no money can still be cancelled until commit.
- **`detached = context.WithoutCancel(ctx)`.** The authorize dispatch runs on it: `tapAuthorize`, covering the dispatch, the tap binding, `ConfirmTap`, verify and settle. So do the error-branch `tapEntry`, `touchReaderLastUsed`, both `reversePaymentLegs` calls (the deferred one and the stale-capture one) and all three `releaseReservedVouchers` calls.
- **`recordCtx`.** This is `detached` once `len(capturedLegs) > 0`, otherwise `ctx`. The fiscal sign ask, the voucher write-through and `pos.CompleteSale` run on it.
- **After commit.** `ctx = detached`, so evidence, audit, table release, publishes and `SaleTotals` survive a disconnect for every method.

The pattern follows #6784 (`refund_page.go` `refundCtx`/`recordCtx`). No ADR was needed: this applies existing precedent. ut-docs `architecture/wasm-runtime.md` gains one sentence on the authorize paragraph.

## Tests

Nine tests. T1–T6 are in `tender_disconnect_test.go`, T7 in `tap_pending_test.go`, T8 in `abandoned_tender_reversal_test.go` and T9 in `voucher_sync_proxy_test.go`.

- **Fail on `main`:** T1 approve-then-cancel records the sale, T2 cancel inside authorize, T3 kiosk, T5 reversal after a non-cancel `CompleteSale` failure, T6 post-commit `sale.completed`, T7 tap, T8 superseded capture on retry.
- **Pass on `main` by design:** T4 is the cash guard (a cancelled cash tender records nothing). T9 guards the end state of a cross-till voucher release.

Test-only seams: the shared fake authorize returns `ctx.Err()` like a real guest, plus a `postTenderCtx` helper and a `primaryCallLog.onCall` hook.

## Verification

- **Dev:** `gofmt`, `go build ./...` and `go vet` pass. `golangci-lint ./internal/pages/...` reports 0 issues. The full `internal/pages` package passes without `-race` (752 s). Guards exit 0: fixed-dates, i18n, core-neutral, kiosk-engine, data-access, mail-addresses, card-data-schema, no-showmodal, no-native-confirm, ci-tests-sandboxed.
- **Tester, red-on-main:** stashed the fix and saw T1–T3 and T5–T8 fail for the stated reasons, for example "sales = 0, want 1" and "reversal got []".
- **Tester, race run:** `go test -race` on the new tests plus `Tender|Tap|Abandoned|VoucherSync` passes (229 s) with no races. The full package was not run with `-race` locally because it takes 90+ min on this box; CI runs it.
- **Mutations:** the Dev sweep, re-checked independently by both Tester and Reviewer, catches each removed detach. The deferred reversal back on `ctx` fails only T5; dropping `ctx = detached` fails only T6; `recordCtx` always detached fails only T4; `tapAuthorize(ctx)` fails T2, T3, T7 and T8.
- **No driven UI run:** this is a backend context change with no template, JS or string changes. A browser cannot time a disconnect between authorize and commit; the handler tests cancel the live request ctx from inside a stub at that point.

## Findings (review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (pre-existing) | `tap_pending.go` `tapUnlock` and `tapCancelThenAssess` reverse or cancel on `r.Context()`. A closed tab during "not charged" resolution leaves the money out and the lock cleared. | Deferred → ut-docs#6908 |
| 2 | major (pre-existing, window now longer) | `/api/pos/tender` has no in-flight guard, and `hx-sync="#basket:replace"` controls abort it. A double Pay with a deduped request id could reverse a recorded sale's payment, and a mid-tender scan is wiped by the final reset. | Deferred → ut-docs#6909 (includes the Architect/Tester UX note) |
| 3 | minor | A cash-only `fiscal.sign.ask` is still cancellable, so a signed TSE transaction can be left without a sale. This is deliberately out of scope; T4 pins the current behaviour. | Deferred → ut-docs#6910 |
| 4 | minor | The `releaseReservedVouchers` call-site swaps don't change behaviour, because `releaseVoucherOnPrimary` already detaches (`voucher_sync_proxy.go:311`). T9 is a guard, not red-green evidence. | Accepted; noted above |
| 5 | nit | The timeout comment named the gate timeouts. | Fixed: now names `WasmRuntime.timeoutFor` |
| 6 | nit | After a disconnect the reader stays marked in use until the plugin call ends, up to 120 s, so a retry sees "reader busy". | Accepted (intended) |

The durable "money moved, record pending" marker for crashes and non-context failures is ut-docs#6653, cross-linked.

Pre-flight: review record updated; `web/help/` n/a (no operator-visible change: the sale that was charged is now recorded); `reference/` n/a (no page describes request contexts); ut-docs `architecture/wasm-runtime.md` updated; `en.json` n/a; README n/a; follow-ups → #6908, #6909, #6910.

## Verdict

Ship after fixes. Fix 5 was applied; 1–3 are deferred to linked cards; 4 and 6 are accepted.
