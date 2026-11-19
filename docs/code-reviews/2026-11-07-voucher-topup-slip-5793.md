# Review — gift voucher slip for a voucher top-up (ut-docs#5793)

Date: 2026-11-07 · Lane: lane:cloud-54 · Branch: `feat/5793-voucher-topup-slip`
Author model: Sonnet (Dev). Reviewer: Opus 5.5, an independent subagent with a fresh context.

## What shipped
- When the default-off "Print each gift voucher on its own slip" setting (`printer.voucher_slip`, #4247) is on, a sale that **tops up** a voucher (#4252) now prints a stand-alone slip for that top-up. The slip carries the top-up label, "+amount", the holder, the new balance right after the top-up (from the ledger, so a reprint matches), and the CODE128 code.
- No slip prints for a reversed top-up (the sale was voided) or for a voucher that is void now (new `data.VoucherTopUp.VoucherVoid`, `v.status = 'void'`, read in `VoucherTopUpsInSale`).
- `print.VoucherBlock.Issued` is renamed `Slip`. Only `voucherSlips` reads it. Receipt bytes are unchanged; the Tester checked byte-identity against `origin/main` in 10 cases.
- The reprint audit's `voucher_slips` count (#5783) includes top-up slips.
- Order: issued vouchers first, then top-ups in the order they were recorded.
- Text: the `settings.printer.voucher_slip_hint` value (same key, no placeholders) and both meta descriptions. Help: `printing.md` (item 10, Reprinting) and `vouchers.md` (top-up step).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low (pre-existing) | The receipt block for a top-up of a since-voided voucher still prints a scannable code | Out of scope (the card requires unchanged receipt bytes). Filed as ut-docs#5794 |
| 2 | low | A replica with a stale mirror (voucher voided on the main till, not synced yet) still prints the top-up slip | Accepted under offline-first; the slip follows local state |
| 3 | nit | New tests ignored errors from `customerReceiptDoc` / `BeginTx`, with no rollback | Fixed: `mustReceiptDoc` helper, checked `BeginTx` plus a deferred rollback |
| 4 | nit | Stale "returns nothing" comment on `seedSlipTopUp` | Fixed |

## Verification
- TDD, Dev: tests were red first (compile errors on `Slip`/`VoucherVoid`; `slips = 0, want 1`; audit `voucher_slips` 2 vs 3), then green.
- TDD, re-verified by the Reviewer: with `print_api.go` at main plus only the rename, `TestPrintReceiptIn_TopUpSlip` fails with `slips = 0, want 1`; restored, it passes.
- Tester: 9 production mutations, each caught. It added `TestVoucherSlips_TopUpBalanceIsHistorical_IssueRow`, because the original historical-balance test only exercised the replica branch.
- Tester visual attestation (text render, AC1): shop name / "Gift voucher top-up +£20.00" / "For Anna" / "New balance £45.00" / "GV-A". In ESC/POS, GV-A prints as a CODE128 barcode followed by a feed-cut.
- Gates after the fixes: `gofmt -l .` clean, `go build ./...`, `golangci-lint` 0 issues, `go test` of print/pages/data ok, and every `ci.yml` build-job guard passes. Three guards could not run locally for environment reasons only; CI runs them: deadcode (local tool built with Go 1.26), shellcheck-version (no binary). `go test ./...` passes except `internal/plugins`, which hits the default 10-minute timeout locally (unchanged by this PR; it passes with `-timeout 30m`).

Pre-flight: help topic updated (`printing.md`, `vouchers.md`); docs-shots n/a (printed output only, no screen changed); en.json value + `en.meta.json` updated (same key, no placeholder change, ADR-0160); reference/ n/a; README n/a; follow-up filed → ut-docs#5794.

## Verdict
Approve. Both nits are fixed; there are no high or medium findings.
