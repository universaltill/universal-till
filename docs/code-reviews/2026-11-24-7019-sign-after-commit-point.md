# ut-docs#7019 - no refusal after a signed fiscal.sign.ask (abandon, voucher, commit guard)

Branch `fix/7019-sign-after-commit-point`. Pipeline: BA, Architect (Opus), Dev (Sonnet), Tester (Opus) and an
independent review (Opus, different model from the author). Companion docs PR: ut-docs `docs/7019-sign-after-commit-point`.

## What shipped
- `completeTender` (`internal/pages/pos_api.go`) now runs: mint SaleID, abandon check, cross-till voucher
  reservations, `BeginTenderCommit`, the pre-ask hook and disconnect check, `fiscal.sign.ask`, the cannot-sign refusal,
  then `CompleteSale`. ADR-0044 D1 (sign after authorize, before persist) holds; the ask payload is unchanged.
- One deferred voucher release replaces three explicit calls; it is disarmed once `CompleteSale` succeeds.
- No abandon check after the ask, by design: while the commit point is held, `BeginAbandon` refuses (the #6923 409).
- `web/help/en/payments.md`: one sentence. No new `en.json` key (the 409 reuses `pos.tender.in_flight`).
- Out of scope: `CompleteSale` failing after a signed ask, sale and refund path (ut-docs#7021). The refund path has
  no R1-R3 equivalent, so it is unchanged.

## Tests
`internal/pages/fiscal_sign_commit_order_7019_test.go`, 9 tests (abandon during the ask returns 409 and the sale is
recorded, cash and card; admitted abandon refused before any ask, cash, card, kiosk; voucher refused before any ask,
cash and card; cannot-sign releases the reserved voucher; a disconnect before the ask releases it).
- Red on main: each of the 9 fails on `c1a630b3` on its assertion (Dev, then re-verified by the Tester in a clean
  worktree and by the reviewer with HEAD~1's `pos_api.go`).
- `-count=20` clean; `-race -count=5` on the 9 clean; full `go test ./internal/pages/` ok (601-641 s), `./internal/pos/` ok.
- Mutants: dropping the deferred release (2 tests catch), commit point after the ask (5 catch), releasing after a
  recorded sale (3 existing voucher tests catch). Abandon check moved after the ask survives but is equivalent
  (the commit point already refuses every abandon).

## Findings (review)
- L1 (low, fixed): the comment at the ask claimed no reset can rotate the attempt; the kiosk landing page's
  `KioskEngine.Reset()` bypasses `abandonOp`. Comment narrowed; pre-existing gap filed as ut-docs#7022.
- L2 (low, fixed in ut-docs): contract said "exactly one of two ways" then listed a third; reworded, changelog too.
- L3 (low, fixed in ut-docs): ADR-0084 note listed only some releasing exits; now lists all.
- N1 (nit, not done): no test combining cannot-sign + captured card leg + reserved voucher. Each part is covered
  separately; left as is.
- N2 (nit, not done): two tests use a 300 ms timer to release a held abandon; the outcome no longer depends on it
  (BeginTenderCommit waits for the admitted abandon); no flakes in 20 runs.
- Exit-path audit clean: every exit after a reservation releases and ends the commit point; no double release; no
  deadlock (ask capped at 3 s, nothing on the ask path takes the commit point); #6602 reversal unaffected.

## Verification
`gofmt -l .` clean, `go build ./...`, `go vet ./...`, `golangci-lint run ./internal/pages/...` 0 issues; 69 of 71
build-job guards pass (not run: `guard-catimport-version.sh` needs catimport tags, `catimport/` untouched; shellcheck
not installed, no scripts changed). No driven app run: server-side ordering only, covered through the real mux.

Pre-flight: no SQL/migration, no new strings, en help only (ADR-0160), no card data, offline-first unchanged (no new network call; reservations already existed).

Verdict: PASS after L1-L3.
