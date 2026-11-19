# Review — abandoned tender reverses captured card legs (ut-docs#6602)

Date: 2026-11-18. Branch `fix/6602-abandoned-tender-reversal`. Lane `lane:cloud-41`.
Built by Sonnet (3 dev rounds), reviewed by Opus 5.5 (different model), two rounds.
Precedent: `2026-11-18-payment-fixes-6600-6602-6606.md` (the deferred half of #6602).

## What shipped
- `internal/pos`: a locked tender (earlier card legs captured, a later leg `outcome_unknown`)
  keeps its captured and unknown legs on the engine (`pending_tender.go`). Every reset path —
  New sale/clear (`ResetAndSnapshot`), re-park, park (`ResetBecause`), resume over the live basket,
  `RestoreHeld` — hands them to a handler the pages layer registers, after the engine lock is
  released. The engine remembers which legs it handed off and whether an attempt is still live
  (`TenderAttemptIs`, `RotateTenderAttemptIDIf`, `NotHandedOff`).
- `internal/pages/abandoned_tender.go`: the handler reverses captured legs through
  `reversePaymentLegs`; the unknown leg is never refunded and gets a Problems item; a failed
  reversal is a Problems item with method, amount, request id and `maskTxnRef`.
- `completeTender`: one `defer` reverses captured legs on every error return after capture
  (closes the fiscal-no-receipt and voucher-reservation gaps); the duplicate-voucher check now runs
  before authorize. It re-checks after each authorize and before settle/reserve/`CompleteSale`
  that the basket wasn't abandoned meanwhile (`tenderAbandonedError`, no sale recorded).
- Cashier sees `pos.toast.tender_abandoned_returned_unknown` / `..._check_provider` at error
  level; `/open-orders/resume` and `/open-orders/counter/open` carry it as `?warn=<known key>`
  (new `httpx.QueryWarnKey`, unknown keys fall back, never echoed). Kiosk maps
  `tenderAbandonedError` to `selforder.checkout.outcome_unknown` (409).
- Help: `web/help/en/payments.md` "When a card payment can't be confirmed".
  ut-docs `reference/payment-provider-contract.md`: full list of reversal triggers.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| T-D1 | med | (Tester) open-orders resume warning rendered as green `?msg=` notice, auto-removed after 2.5 s | fixed: `?warn=` error banner, tests on both routes |
| T-D2 | high (CI) | docs-shots surface hash stale after `internal/pages` change | fixed: hash updated, no screenshotted pixel changes (`Docs-Shots-Unchanged: true`) |
| M1 | med, money | abandon racing an in-flight locked retry could reverse a leg twice or record a sale whose leg was refunded | fixed: hand-off memory + live-attempt re-checks; `TestTender_AbandonedDuringRetry_*` (blocking fake plugin, concurrent reset, `-race`) failed before, pass after |
| L1 | low | cannot-sign log said "reversed" before the defer ran | fixed |
| L2 | low | help said clear/park happens "straight away" (it waits for each refund) | fixed |
| L3 | low | a reset landing between the re-check and `LockTender`/`SettleTender` (no I/O in the window) could still re-lock the next basket | accepted; deferred → follow-up card |
| L4 | low | kiosk showed generic "try again" for `tenderAbandonedError` | fixed (one-line mapping); no kiosk test for the mapping → follow-up card |

## Verification beyond unit tests
- Tester full gate (round 1): gofmt, build, vet, `golangci-lint` 0 issues, shellcheck, `go test -timeout 30m ./...` (120 packages ok), 175/179 build-job guards (4 = T-D2 + missing shellcheck binary, both resolved).
- Rounds 2–3: `go test -race ./internal/pos`, `-race` pages subset `Abandon|Tender|SaleScreen|OpenOrders|Hold`, full `./internal/pages`, `./internal/httpx`; guards i18n, i18n-meta, help-topics, kiosk-engine, docs-shots, no-showmodal, no-native-confirm, fixed-dates, data-access all rc=0 on the final tree.
- Mutations killed: paths A, B, C, D, E hand-offs, the defer, attempt rotation, superseded-capture reversal, duplicate-voucher move, the M1 re-check and hand-off filter (each pinned by a different test).
- Driven run: the locked state is not reachable on a running till without an installable plugin answering `outcome_unknown`; the error toast was rendered through the real basket template at 1024x600 and 360px (doesn't cover Pay/total, no horizontal scroll, stays >6 s). German pack lacks the new keys (Weblate) — wrapping checked with a German-length stand-in. Dark theme / RTL not looked at.

Pre-flight: help topic `web/help/en/payments.md` updated; `make docs-shots` n/a (no screenshotted pixel changed, surface hash updated); `reference/payment-provider-contract.md` updated (ut-docs); `README.md` n/a (no stale text); new `en.json` keys have `en.meta.json` entries; follow-ups deferred → Backlog card (L3 window, kiosk mapping test, unresolved-unknown-leg status chip, #6601 restart persistence stays on its card).

## Verdict
Approve after round-2 re-check. #6602 closes with this PR; #6601 and #6603 stay open on their own cards.
