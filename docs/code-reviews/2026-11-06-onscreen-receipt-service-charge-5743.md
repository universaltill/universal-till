# 2026-11-06 — on-screen receipt: service-charge row (ut-docs#5743)

**What shipped.** The printed receipt (`internal/pages/print_api.go`
`buildReceiptDocIn`) already prints a **Service charge** row whenever the
sale's `sales.service_charge_amount` is non-zero. The on-screen receipt
(`web/ui/partials/receipt.html`, `receipt_body`) had no such row, so on a
sale with a service charge Subtotal + Tax (+ Vouchers issued) did not add up
to Total on screen.

- `renderReceipt` (`internal/pages/pos_api.go`) takes a trailing
  `serviceCharge int64` and puts it in the template data as `ServiceCharge`.
  The tender handler passes `saleInput.ChargesTotal().Minor()`.
- `receipt_body`'s totals now run in the printed order: Subtotal, Discount,
  Tax, Service charge, Vouchers issued, Total. The Discount row moved from
  after Tax to between Subtotal and Tax (its condition and markup are
  unchanged). The new row (`data-testid="receipt-service-charge"`) reuses
  the existing `journal.detail.service_charge` key and, like the printed
  row, is not gated by **Show subtotal and tax lines**.
- Every existing `renderReceipt` test call passes `0` for the new
  parameter. New tests: `internal/pages/receipt_service_charge_5743_test.go`
  (row present, amount, order, with ShowTax on and off; absent at zero).
- `web/help/img/manifest.json`: `surface_sha256` refreshed (see F1).

**Pre-flight:** web/help topic: n/a (no topic enumerates the on-screen
totals rows; `designer.md`'s "Show subtotal and tax lines" text stays true,
since the service-charge row is deliberately not under that toggle).
`make docs-shots`: n/a (no docs-shots spec completes a sale, so the
on-screen receipt is in no screenshot; the designer preview renders the
print document, not `receipt.html`): surface hash refreshed with
`update-docs-shots-surface-hash.sh`; commit carries
`Docs-Shots-Unchanged: true`. reference/ page: n/a. README: n/a. en.json
keys: n/a (existing key). Lang packs: n/a. ADR: n/a. Review record: this
file. Follow-ups: none.

**Review** (independent tester + reviewer, Opus 5.5; Dev was Sonnet).

Checked and fine:

- **Same value as print.** `saleInput.Charges` is built once in the tender
  handler (`pos.BuildCharges`) and never reassigned; `completeTender` and
  `pos.CompleteSale` take `SaleInput` by value. `computeSaleTotals` sets
  `serviceCharge = SumCharges(in.Charges)` and `InsertSale` persists that as
  `sales.service_charge_amount`, which `print_api.go` reads back as
  `detail.ServiceCharge`. `ChargesTotal()` is `SumCharges(in.Charges)`, so
  screen and paper show the same figure. (The on-screen row covers the sum
  of all charges — merchant service charge plus any plugin levy — exactly as
  the printed row does.)
- **Order** matches `print_api.go`'s `doc.Totals` append order row for row,
  including both ShowTax gates and the ungated discount/service-charge rows.
- **Other renderers of `receipt_body`.** Only two: `renderReceipt` and
  `renderReceiptPrintCopy` (ut-docs#4730), which copies the same data map,
  so the customer-language print copy gets the row too. No reprint, journal,
  kiosk or self-order path executes `receipt.html`. A data map without the
  key would still render (missing map key is falsy in `{{ if }}`).
- **Refunds / negative charge.** `renderReceipt` runs only on the sale
  tender path; refunds use their own screens. `computeSaleTotals` rejects a
  negative charge per item (ADR-0062), so the value is `>= 0`; `{{ if
  .ServiceCharge }}` is true for any non-zero int64, same as print's `!= 0`.
- **i18n/RTL.** Reused key (present in `en.json`); same inline
  "label: amount" pattern as the neighbouring rows; no CSS added.
- **Tests are real.** The order assertions fail if the Discount row is put
  back after Tax or the row is moved after Total; the zero test fails if the
  condition is dropped. No fixed dates; no real shop names ("My Store").

Findings:

- **F1 (must-fix) — fixed.** `guard-docs-shots.sh` (via
  `run-docs-shots-guard.sh`) failed rc=1: `receipt.html` and
  `internal/pages/*.go` changed the surface hash. Confirmed no docs-shots
  screenshot renders the on-screen receipt, ran
  `scripts/ci/update-docs-shots-surface-hash.sh` (one-line
  `surface_sha256` change in `web/help/img/manifest.json`); guard now rc=0.
  Commit needs the `Docs-Shots-Unchanged: true` trailer.
- **N1 (nit) — accepted.** The on-screen value comes from the in-memory
  `SaleInput`, the printed one from the stored column. They are the same
  computation today (above); reading `service_charge_amount` through
  `POSRepo.SaleTotals` would make it one source, but that widens a repo
  signature used elsewhere for no behavioural gain. Same trade-off as #5739
  N1.

**TDD re-verified** (inline, on the working tree, within one turn):
`git stash push web/ui/partials/receipt.html` →
`go test ./internal/pages/ -run 5743 -count=1` →
`--- FAIL: TestRenderReceipt_ServiceChargeRowOrdered_5743 …
receipt_service_charge_5743_test.go:39: ShowTax=true: no service-charge row`
→ `git stash pop` → both 5743 tests PASS.

**Look at it.** Not looked at in a browser (no Playwright/driven run in this
cloud session); verified by rendered-HTML assertions only. Not checked: dark
theme, kiosk sizes, RTL and German rendering of the row (it reuses the
neighbouring rows' markup, so no new layout).

**Gate** (after the last edit, Go 1.27.1): `gofmt -l .` empty;
`go build ./...` ok; `go vet ./...` ok; `golangci-lint run ./...` 0 issues;
`shellcheck scripts/ci/*.sh` rc=0 (shellcheck 0.11.0 installed for this
run). `go test ./...`: 108 packages ok, including `internal/pages` (376 s);
`internal/plugins` hit the 10 m package timeout while running in parallel
with the guard loop (`TestHTTPSettingBoundGrant` was in flight; it passes
alone in 28 s). A solo full-package re-run was still going after 10 m and
was stopped. This diff touches no `internal/plugins` file, and that package
does not import `internal/pages`. Same sandbox behaviour as noted in
#5739's record; CI's test-heavy job is the real check. Every
`bash scripts/…` step of `ci.yml`'s `build` job (143 scripts) rc=0,
including `run-docs-shots-guard.sh` after F1. `guard-shellcheck-version.sh`
first failed for lack of a binary, then passed once shellcheck was installed.
Not run locally: `windows-signing_test.sh` (needs sudo apt/nsis), and the
non-script `sdk/plugin` and `catimport` module steps (untouched).

**Verdict.** Approve (F1 fixed).

**Deferred.** None.
