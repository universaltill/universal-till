# Review: printed receipt labels follow the shop's locale (ut-docs#4199)

**Date:** 2026-10-14 · **Lane:** lane:cloud-41 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped

`buildReceiptDoc` (`internal/pages/print_api.go`) printed its labels as hardcoded English. The affected labels were the receipt/order number, REFUND marker, original receipt, VAT number, mixed dine-in/takeaway marker, per-line mode, Subtotal/Discount/Tax/Service Charge/TOTAL, payment methods, Change/Tip, the three unsigned-TSE notices, and the TSE and fiscal-device evidence lines. Every one of them now follows the shop's locale (`httpx.DefaultLocale()`), which is the convention the printed invoice already used.

- **Existing keys are reused where the en value is byte-identical:** `basket.subtotal`, `basket.discount`, `basket.tax`, `journal.detail.service_charge`, `receipt.change`, `receipt.tip`, `receipt.fiscal.unsigned_*`, and `tender.{cash,card,voucher}`.
- **20 new `receipt.paper.*` keys** cover the rest. They are printf formats, so a language can reorder the value, and they are in en/ar/fa/tr. Their en values are the old literals, so an en receipt is unchanged.
- **New `receiptMethodLabel`:** a built-in method id maps to its `tender.*` key, and any other id gets `titleFirst(id)`. A side effect is that an empty method no longer panics on `p.Method[:1]`.
- **Digits stay forced Latin**, as before.
- **Manual:** `web/help/{en,de,tr,fa,ar}/printing.md` now says customer receipts follow the till's language.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | fa `receipt.paper.vat_no` used bare "شماره" while its siblings use the ezafe "شمارهٔ" | Fixed |
| 2 | minor | The seed never exercised the `unsigned_fiscal_cannot_sign` notice, so a regression there would pass | Fixed: a third seeded sale carries it. The en test pins it verbatim and the tr/fa test rejects its English. Verified by reverting that one line: the test fails. |
| 3 | nit | The closure-local `Tf` printf wrapper re-invents what `buildInvoiceDoc` does by hand; a shared `httpx.Tf` would be cleaner | Accepted. Out of scope. |
| 4 | nit | 20 new en keys mean `lang-pack-drift` goes red on `main` until de/es/pt carry them | Handled: the pack PRs are prepared and land in the same cycle, after core |
| 5 | nit | No review record yet | This file |

The reviewer checked these and found them clean:
- en is byte-for-byte unchanged for every label.
- No locale is missing a key.
- Every key has the expected printf verb count: 15 with one `%s`, 5 with none, in core and all three packs.
- No user data is ever used as a format string.
- No English literal is left in `buildReceiptDoc`.
- The Windows-1254 encoder genuinely errors on an unencodable rune, so the encoding test can fail.
- Not applicable: a write without `os.MkdirAll`, and a cwd-relative path.

## Verified beyond unit tests

- **TDD:** on the old `print_api.go`, `TestReceiptPrintLabels_EnglishUnchanged` passes, which proves the pinned strings are the old output, and the tr/fa test fails with 37 errors. On the new code, every test passes. The reviewer reproduced this independently.
- **End to end:** `TestReceiptPrintLabels_TurkishReprintBytesOnTheWire` sends the journal reprint (`POST /api/print/receipt/{no}`) through the real mux to a network printer, which is a local TCP listener. For a tr shop on EUR, the bytes on the wire are Windows-1254 Turkish labels and contain no English. It fails on the old code.
- **Packs:** de, es and pt pass `check-key-drift.sh` against this branch's en.json. es and pt add `receipt.paper.total` ("TOTAL") to `same-as-en`. The de/es/pt labels encode in cp858.
- **Gate:** `go build ./...`, `go vet`, `golangci-lint` (0 issues), every guard in the `build` job, and `go test ./...` all pass.
  - `guard-shellcheck-version` was not run green: there is no shellcheck binary in this container, and no shell script changed.
  - `internal/plugins` hit the default 10-minute timeout while building a wasm guest in this container. It is an untouched package and is re-run separately.
- **Not seen:** a physical thermal printout. ESC/POS bytes were asserted, not looked at on paper.

## Deferred (Backlog cards)

- ut-docs#4330: the printed Z-report / end-of-day ticket still prints English labels.
- ut-docs#4331: the receipt designer's sample ticket still prints English labels.

## Verdict

Safe to merge.
