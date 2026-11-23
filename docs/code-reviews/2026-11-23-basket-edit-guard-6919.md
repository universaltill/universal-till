# Review: basket edits and card checks can't change a basket mid-payment (ut-docs#6919)

Date: 2026-11-23. Lane: `lane:cloud-41`. Author model: Sonnet (Dev).
Independent reviewer: Opus 5.5, fresh context, read-only (overlay TDD checks).
Tester: separate agent (mutation checks + driven run). Verdict: **approve with
should-fixes**; S2, S3 and nits N1, N2, N3 (voucher row) fixed by the
orchestrator; S1 deferred to its own card.

Pre-flight: till user-visible behaviour: one help sentence (`web/help/en/sell.md`),
no new strings, no layout change; a refused edit no longer adds the generic
server-error banner on top of the existing "Payment in progress" notice.

## What shipped (checklist items 1 and 4 of #6919)

- `internal/pos/service.go`: `BeginBasketEdit` / `EndBasketEdit` keep a count of
  running basket edits under `s.mu`; an edit is refused while a tender holds the
  engine. `TryBeginTender` (same signature) sets the flag, which stops new
  edits, then waits up to `tenderDrainTimeout` (10 s) for edits already running;
  on timeout it drops the claim and the tender gets the usual 409. No engine
  mutator changed, so the tender's own engine calls need no exemption.
- `internal/pages/tender_inflight.go`: `basketEdit(d, h)` wraps 8 routes with
  the existing 409 `tender_in_flight` (`/api/pos/remove`, `age-check`, `line`,
  `discount`, `order-type`, `table`, `scan-with-modifiers`,
  `add-by-hand/dismiss`). Voucher top-up takes the claim inline, only around its
  basket change (after the permission check and the replica's call to the main
  till — review S3).
- `scan` admits through `BeginBasketEdit` before `SetCustomerID`, closing the
  check-then-act race (keeps its 200 + toast refusal).
- Item 4: `/pay/tap/check` and `/manager/pay/tap/resolve` hold `TryBeginTender`
  for their run, so a locked-tender retry that starts meanwhile is refused
  instead of re-authorizing the leg beside them; both re-read the locked leg
  after the claim (N1). `/api/pay/attempts/attention/resolve` already refuses the
  till's locked leg (`staffLockedKeys`), so no change there.
- `web/public/app.js`: `htmx:responseError` skips the generic banner for a 409
  carrying the `tender-in-flight` trigger (S2).

**Deliberate deviation from the design:** reset, hold, resume, open-orders resume
and counter/open are not refused: they are the cashier's abandon path for a
payment that never answers (#6602; refusing them broke
`TestTender_AbandonedDuringRetry_ApprovalDoesNotCompleteSale`).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix (pre-existing, #6602) | After the tender's last `abandonedUnder()` check, `CompleteSale` commits before `engine.Reset()`; a Hold/Clear in that window parks the sold lines (resell) or writes a false `cleared_baskets` void. | Deferred: new Backlog card (commit-point mark that reset/hold/resume respect). Narrow window; not widened by this diff. |
| S2 | should-fix | Refused edits showed the generic server-error banner too (htmx `responseError`). | Fixed in `app.js`; verified in Chromium against the real binary: banner shown without the fix, hidden with it; notice shown, basket unchanged. e2e `basket-edit-refused-6919.spec.ts`. |
| S3 | should-fix | Top-up wrapped whole: 2 s replica call inside the claim, nil engine panic before its 500, 409 before 403. | Fixed: inline claim before `HasGiftVoucherCode`; table test seeds a real voucher so the row reaches the claim. |
| N1 | nit | Tap check/resolve used the leg read before a claim that can wait. | Fixed: re-read under the claim. |
| N2 | nit | Help sentence too narrow. | Fixed. |
| N3 | nit | "Basket unchanged" assertions mostly vacuous (key `x`). | Partly: 409 envelope is the real assertion (mutation-checked); left as is. |
| N4/N5 | nit | Silent unmatched `EndBasketEdit`; unsynchronised test var. | Left (safe; no `t.Parallel` in `pos`). |

## Verified beyond the guards

- Tester mutation checks (throwaway copy, restored byte-for-byte): neutering
  `basketEdit` fails 9/9 table rows; removing the drain fails 3 pos + 2 pages
  tests; tap check/resolve back to `TenderInFlight()` fail their tests; scan
  admission reverted exposed a gap → Tester added `TestTender_WaitsForRunningScan`
  (fails on the reverted code, 5/5 under `-race` on the fix).
- `go test ./internal/pos/... -race` ok; `go test ./internal/pages/...` (full,
  no `-race`, 594 s) ok; targeted pages tests under `-race` ok after the fixes;
  gofmt, build, vet, golangci-lint 0 issues; 20 CI guards ok (deadcode guard not
  runnable here: toolchain mismatch in the tool itself).
- Driven run (real binary, demo seed): reset, scan, qty, order-type, cash tender
  (receipt in 9 ms, no drain delay), scan after the sale. A refusal while a card
  tender is held was not driven (needs a blocking card plugin); covered by
  httptest tests on the real mux and a migrated DB.
