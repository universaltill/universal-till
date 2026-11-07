# Review: journal "Print without voucher slips" (ut-docs#5838)

Date: 2026-11-07 · Lane: lane:cloud-54 · Card: universaltill/ut-docs#5838 (split from #5744 item 2)

## What shipped

- `printReceiptWith(ctx, d, receiptNo, loc, receiptPrintOpts{skipSlips})` holds the old
  `printReceiptIn` body; `printReceiptIn` is a one-line wrapper, so auto-print and every
  existing caller are unchanged.
- `POST /api/print/receipt/{receiptNo}`: form value `no_slips` exactly `"1"` leaves the gift
  voucher slips out; the `receipt_reprint` audit row then carries `slips_skipped: true` (and no
  `voucher_slips`). Any other value is a plain reprint.
- `/journal/{receipt}`: a second button **Print without voucher slips** (`btn secondary`, same
  `#reprint-msg` result span), shown only when `printer.voucher_slip` is on and
  `slipBearingVouchers` counts at least one slip (non-void issued voucher, or a top-up not
  reversed whose voucher is not void). The voucher read is now also gated on the setting, since
  the receipt doc reads `vouchers.issued_sale_id` regardless of ledger rows.
- New key `journal.reprint_no_slips` (+ `en.meta.json`); help `printing.md` "Reprinting a
  receipt" describes both buttons; docs-shots surface hash refreshed (`Docs-Shots-Unchanged`,
  the journal detail page is not screenshotted). ut-docs `architecture/receipt-printing.md`
  updated in ut-docs PR (branch `docs/5838-reprint-no-slips`).

## Review (independent, Opus 5.5; code by Sonnet)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | major | `web/help/img/manifest.json` surface hash stale → `guard-docs-shots.sh` red on main | fixed (surface-hash script, trailer) |
| F2 | minor | ut-docs `architecture/receipt-printing.md` Reprint bullet silent on `no_slips` / `slips_skipped` | fixed (ut-docs PR) |
| F3 | minor | journal detail reads full `printerConfig` and, with slips on, two un-indexed voucher reads per view | accepted: one receipt per page, the print path already pays the same cost |
| F4 | nit | button hidden if either voucher read fails | accepted: safe direction |
| F5 | nit | a refused (shadow) reprint's audit row omits `slips_skipped` | accepted: nothing printed |
| F6 | nit | `reports.md` receipt-detail section names only **Print receipt** | accepted: `printing.md` is the reprint section and is linked from there |
| F7 | nit | T7 helper `voidTopUp` voids the whole sale; no mixed case | accepted |

Slip parity: the reviewer traced `issuedVoucherBlock` + top-up block + `voucherSlips` and found
`slipBearingVouchers` equal; T7 compares the button against `printReceiptIn`'s real slip count.

## Tester

Real driven run (fresh DB, printer to a device file, slip setting on, gift voucher sold through
the sale screen): **Print receipt** wrote receipt + 1 slip (2 inits/2 cuts), audit
`{"ok":true,"voucher_slips":1}`; **Print without voucher slips** sent `no_slips=1`, wrote the
receipt only, audit `{"ok":true,"slips_skipped":true}`; failed print → inline "✗ Print failed",
audit `ok:false, slips_skipped:true`. Button absent with the setting off and on a voucher-free
sale. 51px tall, focus ring, no horizontal scroll at 1024x600 / 360px, long label wraps, fa RTL
mirrored. Not verified: dark theme, a real de pack, physical printer.

Tester mutation run found two surviving mutants, both fixed with tests and re-verified red→green
by the orchestrator:
- T-1 dropping `slipsOn &&` from the visibility rule → new `TestJournalDetail_NoSlipsButtonHiddenWithSettingOff` (ledger-backed sale) now fails.
- T-2 dropping the button's `hx-vals` → visibility test now asserts the button sends `no_slips`.

TDD: first run failed to build (`undefined: printReceiptWith`, `undefined: receiptPrintOpts`);
reviewer mutations (drop `!t.VoucherVoid`, force `skip := false`) each fail the suite.

## Gates

gofmt clean; `go build ./...`; `go vet`; `go test ./...` green (Tester; `internal/plugins`
needed a longer timeout under load); golangci-lint `./internal/pages/...` 0 issues; every
`ci.yml` build-job guard passes locally except `guard-deadcode-baseline` (local tool built with
go1.26) and `guard-shellcheck-version` (no shellcheck here) — environmental, CI runs both.

Pre-flight: help topic `printing.md` updated; docs-shots surface hash updated (no pixel change);
`reference/` → ut-docs `architecture/receipt-printing.md` updated; `en.meta.json` updated;
README n/a (no claim changed); review record this file.

## Verdict

Approve. Deferred: none filed (F3 indexes judged not worth a card at one receipt per view).
