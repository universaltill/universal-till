# Review: a signed cash tender or refund survives a client disconnect (ut-docs#6910)

Date: 2026-11-24. Lane: `lane:cloud-41`. Independent review by a different model from the author.
Branch: `fix/6910-cash-sign-detach`. Docs half: ut-docs `docs/6910-cash-sign-detach`.

## What shipped
- `internal/pages/fiscal_sign_hook.go`: `dispatchFiscalSignAsk` detaches from the request context right
  after the no-signer return, so a client disconnect can't cancel the ask or the reads that build it
  (sign-start transaction id, order id). It returns `Asked`, which is true only when a signer was really asked
  (not for no-signer or the known-offline short-circuit).
- `internal/pages/pos_api.go` (cashier and kiosk `completeTender`) and `refund_page.go`: if the client is
  already gone just before the ask, the tender or refund is refused and nothing is recorded or signed. Once a
  signer was asked, the rest of the recording runs detached, so a signed cash sale or refund is always recorded
  (ADR-0044 Decision 1; ADR-0056 forbids re-signing later). `cannot-sign` refusals (ADR-0136, ADR-0146) are
  unchanged. With no signer, a gone client still records nothing, as before.
- Tests: `tender_disconnect_test.go`, `refund_disconnect_test.go`, `fiscal_sign_hook_test.go`. The old
  `TestTender_ClientDisconnectCashTenderRecordsNothing` and its refund twin cancelled after signing and pinned
  the bug. They are inverted to `...AfterCashSignStillRecordsSale` / `...AfterCashSignStillRecordsReturn`.
  New tests cover a gone client before the ask (through test-only hooks right before the check), no signer,
  cannot-sign, backend failure, a wedged signer bounded by the ask budget, and `Asked` (including known-offline).

## Findings (independent review)
- **M1 (major), fixed.** The "gone before the ask" tests cancelled before the request, so the handler failed
  early (age-restricted read) and never reached the new pre-check: deleting the check left them green. Fixed with
  `tenderBeforeFiscalSignHook` / `refundBeforeFiscalSignHook`. Re-verified by the orchestrator: with the tender
  check disabled, `tender_disconnect_test.go:209: fiscal.sign.ask answered 1 times, want 0`; restored, it passes.
  The refund Before test fails the same way at `refund_disconnect_test.go:103`. The no-signer test cannot go red
  on the pre-check (`CompleteSale` already fails on the cancelled ctx); it stays a regression guard.
- **m1 (minor), fixed.** A disconnect between the pre-check and the ask could cancel the sign-start / order-id
  reads and sign an incomplete payload. The detach now happens right after the no-signer return.
- **m2 (minor), fixed.** The contract claimed every asked sale is recorded or refused by cannot-sign. Three
  existing paths can still refuse after a signed ask (abandoned basket, voucher reservation refusal,
  `BeginTenderCommit` false). The wording is narrowed, and those paths are now tracked on ut-docs#7019.
- **Nits, fixed.** The changelog row says "Behaviour change, no wire change", the lead-in sits next to its list
  again, and `wasm-runtime.md` adds "unless it answers `cannot-sign`".
- No problems in: the 3 s ask budget (kept, wall-clock test), the detached path through commit, lock and
  voucher state on the new refusal, the kiosk path, refund parity, i18n (existing keys only), money, and file
  writes and paths (none).

## Verification
- Fail on `main` (Tester, done personally): AfterCashSignStillRecordsSale (0 sales), BackendFailureDeclares,
  WedgedSignerStaysBounded, Refund AfterCashSignStillRecordsReturn (0 returns), and the `Asked` unit test.
- Gate (Tester): gofmt clean, `go build ./...`, `go vet ./...`, golangci-lint 0 issues, and `go test ./...` pass.
  `internal/plugins` needs `-timeout 35m` on this box; it is untouched by this change. `-race` on
  `internal/pages -run 'Fiscal|Tender|Refund|Disconnect'` passes. `-count=5` on the touched tests passes. The
  `ci.yml` build guards pass, except three that fail only for the environment here (go1.27 deadcode, shellcheck
  version, qemu). After the fix round: gofmt, build and vet are clean, and the touched tests pass.

Pre-flight: review record updated; `reference/contracts/fiscal-sign-ask.md` 1.17.1 + `architecture/wasm-runtime.md`
updated (ut-docs); `web/help/` n/a (no screen or flow changes: a disconnect mid-tender is not an operator action);
`en.json` n/a (existing keys only); README n/a; follow-up deferred → ut-docs#7019.

Deferred (non-goals): a core-side TSE cancel or void (ADR-0077 D1, #2079); a crash between sign and commit
(#6653); reusing a signature when a lost response is retried.

Verdict: ship.
