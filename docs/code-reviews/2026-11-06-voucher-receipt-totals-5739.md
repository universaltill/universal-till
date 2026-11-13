# 2026-11-06 — receipt totals: "Vouchers issued" row so voucher sales add up (ut-docs#5739)

**What shipped.** A sale that issues multi-purpose gift vouchers keeps their
face value in `sales.total` and out of `subtotal` and `tax_total`. That is
deliberate: the voucher is a liability, not revenue (ut-docs#1008,
`pos.computeSaleTotals`). The receipt's totals rows therefore did not add up.
A voucher-only sale printed "Subtotal £0.00" above "TOTAL £35.00".

Both receipts now have a **Vouchers issued** row after service charge and
before TOTAL. It reuses the existing `receipt.vouchers.issued` key, which the
journal detail page already uses for this row (ut-docs#4256). The row appears
only when the voucher amount is non-zero and **Show subtotal and tax lines**
(`ShowTax`) is on, the same gate as the rows it reconciles.

- Printed and reprinted receipts (`internal/pages/print_api.go`
  `buildReceiptDocIn`): the value comes from `detail.VoucherIssueTotal`
  (`sales.voucher_issue_total`).
- On-screen receipt (`web/ui/partials/receipt.html`, `renderReceipt` in
  `internal/pages/pos_api.go`): the value is summed from the issued vouchers
  that are not single-purpose, using the same rule as `computeSaleTotals`.
  Top-ups count because they are always multi-purpose.
  `receiptVoucherIssue` gains `SinglePurpose`.

The Z report already accounts for voucher liability in its own section, so
it is unchanged (AC3; checked in `eod_api.go` and `eod_tax_bands.go`). New
tests are in `internal/pages/receipt_voucher_totals_5739_test.go`.

**Pre-flight:** web/help topic: updated (`vouchers.md` step 6
and `designer.md` "Show subtotal and tax lines" now cover a further row).
`make docs-shots`: n/a (no Playwright in this cloud session; no docs-shots spec renders a voucher sale): surface hash refreshed with `update-docs-shots-surface-hash.sh`, the two edited `en` topic hashes (designer, vouchers) set to their sha256 exactly as `write-manifest.js` computes them; `guard-docs-shots.sh` passes; commit carries `Docs-Shots-Unchanged: true`.
reference/ page: n/a. README: n/a. en.json keys: n/a (existing key,
baselined in `key-meta-baseline.txt`). Lang packs: n/a. ADR: n/a. Review
record: this file. Follow-ups: see Deferred.

**Review** (independent, Opus 5.5; Dev was Sonnet 5.5). I read the code paths and
ran the 5739 tests (pass), `go vet ./internal/pages/`, gofmt, and these guards:
i18n, i18n-meta, rtl-logical-css, fixed-dates, data-access, no-showmodal and
help-topics (all rc=0). `guard-docs-shots` had rc=1 (see F1).

TDD re-verified in a throwaway worktree at HEAD, without the fix and with only
the new struct field added. All three receipt tests failed: voucher-only,
mixed, and on-screen.

Findings:

- **F1 (must-fix) — fixed.** `scripts/ci/guard-docs-shots.sh` fails
  (rc=1) because `web/ui/partials/receipt.html` and two `internal/pages/*.go`
  files changed the surface hash. No docs-shots spec renders a voucher sale,
  and the row only appears for one. Refresh the hash with
  `scripts/ci/update-docs-shots-surface-hash.sh` and add a
  `Docs-Shots-Unchanged: true` trailer, or run `make docs-shots`.
- **F2 (should-fix) — fixed** (asserts on `on[vi:ti]`; top-up and single-purpose entries added; mutation re-checked). False pass in the on-screen test.**
  `TestRenderReceipt_TotalsShowVouchersIssued_5739` checks
  `strings.Contains(on[vi:], "$35.00")`. That substring runs past the voucher
  row into the TOTAL row, which is also $35.00. I confirmed this with a
  mutation: the row rendered `{{ money 1000 }}` ($10.00) and the test still
  passed. Fix: assert on `on[vi:ti]`.
- **F3 (should-fix) — fixed** (option 2: `newReceiptVoucherIssue` + `receiptVoucherIssueTotal` helpers, `TestReceiptVoucherIssueTotal_SinglePurposeNotCounted_5739`; dropping the assignment fails it). The `SinglePurpose` wiring is untested.**
  It is set in the tender handler (`pos_api.go:2765`,
  `v.Purpose == data.VoucherPurposeSingle`). The test calls `renderReceipt`
  with a hand-built slice, so the test still passes with the assignment
  removed. If it regressed, a sale with a single-purpose voucher would show
  a Vouchers issued row and double-count on screen.
  - Fix option 1: a tender-endpoint test with one single-purpose and one
    multi-purpose voucher.
  - Fix option 2: extract the slice builder and test it.
  - Also add a top-up entry to the on-screen test, because top-ups are
    deliberately counted.
- **F4 (should-fix) — fixed** (English only; help guards pass). The manual does not mention the new row.**
  - `web/help/en/vouchers.md` step 6 describes the receipt's voucher block
    but not the totals row.
  - `web/help/en/designer.md` says the ShowTax toggle controls "subtotal and
    tax lines". It now also hides this row.
  - `reports.md:546` already describes the row for the journal.
- **N1 (nit) — accepted/backlog. Two values for one fact.** The on-screen
  value is recomputed from the in-memory issue list. The printed value is
  read from `sales.voucher_issue_total`. They agree today because both apply
  `computeSaleTotals`'s rule. Reading the stored column through
  `POSRepo.SaleTotals` would leave one source of truth.
- **N2 (nit) — pre-existing, out of scope.** The on-screen receipt has no
  service-charge row, so a voucher sale with a service charge still does not
  add up on screen. The printed receipt is fine. Candidate Backlog card.
- **N3 (nit) — accepted (design). VAT-registered shop with ShowTax off.** The
  printed receipt shows TOTAL plus per-rate "Total incl. VAT" bands. The
  voucher row stays hidden, so the bands sum to less than TOTAL. This is
  unchanged from before and follows the Architect's gate.
- **N4 (nit) — accepted. Same label twice.** "Vouchers issued" is now both a
  totals row and the heading of the voucher-codes block on the same receipt,
  printed and on screen, as on the journal page.
  - The baselined key has no meta description, so translators cannot see
    that it serves both uses.
  - The new `data-testid="receipt-voucher-total"` sits inside
    `receipt_body`, which renders twice when a customer-language print copy
    is present. That matches the existing `receipt-mixed` precedent.

Checked and fine:

- Top-ups are in `voucher_issue_total`, because `computeSaleTotals` skips
  only `VoucherPurposeSingle`. The printed and on-screen receipts both count
  them, so a top-up sale adds up on both.
- Returns cannot issue vouchers (`sales.go:900`), so the stored total is 0
  and no row appears. A negative value is not possible.
- A reprint, or the receipt of a voided sale, reads the same stored column.
- The int64 truthiness in `{{ if and .ShowTax .VoucherIssueTotal }}` is
  correct.
- `renderReceiptPrintCopy` copies the data map, so the customer-language
  print copy gets the row too.
- RTL: the row uses the same inline "label: amount" pattern as the
  surrounding rows and has no physical CSS.
- No fixed dates in the tests (`datetime('now')`).
- The voucher-slip block (#4247) is untouched.

**Verdict.** Approve after fixes (F1–F4 fixed; fixes re-verified by the orchestrator: 5739 tests pass, docs-shots guard passes).

**Deferred.**

- N1, single source for the on-screen voucher total: optional card.
- N2, on-screen receipt lacks a service-charge row: proposed card.

**Tester** (Sonnet, separate agent): PASS. Revert proof per production file; `go test` of every package except `internal/plugins` ok (`internal/plugins` hangs in its TCP fixture identically on clean `main` in this sandbox). Driven run on a real till binary with a device-file printer: voucher-only sale (£25 + £10, cash) printed and showed Subtotal 0.00, Tax 0.00, Vouchers issued 35.00, TOTAL 35.00; mixed sale (item £2.40 incl. £0.40 tax + £25 voucher) showed Subtotal 2.40, Tax 0.40, Vouchers issued 25.00, TOTAL 27.40. Only the light theme at 1280×800 was looked at; on screen the row wraps onto two lines in the narrow totals box (cosmetic).

Gate not runnable here: `guard-deadcode-baseline` (local Go 1.26 vs required 1.27), `guard-shellcheck-version`/shellcheck (no binary; no scripts changed), network-only ADR taxonomy guards — left to CI.
