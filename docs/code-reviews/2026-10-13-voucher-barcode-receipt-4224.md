# Review — gift voucher block with barcode on the receipt (ut-docs#4224)

**Shipped**
- `POSRepo.VouchersIssuedInSale` (`internal/data/voucher_repo.go`): every voucher a sale issued, in basket order (`created_at, rowid`), void ones included.
- `print.Doc.Vouchers` / `VoucherHeading` (`internal/print/escpos.go`): after the payments, per voucher the value, "For <holder>", and the code as a CODE128 symbol (its HRI is the text). A void voucher prints the void notice and its code as text only, never as a symbol. A code the symbol can't carry (non-ASCII, >60) prints as text. `RenderText` (system printer, designer preview) prints the same block with the code as text and no placeholder bars. No vouchers: output byte-identical to before.
- `buildReceiptDoc` fills the block from the DB, so a reprint matches; labels via `httpx.T(printLocale, …)` reusing `receipt.vouchers.issued`, `catalog.item_type.gift_voucher`, `basket.gift_voucher.for`; one new key `receipt.vouchers.void` (en/ar/fa/tr here; de/es/pt pack PRs follow the merge).
- On-screen receipt: holder line + Code39 SVG under each code, CSS so number and value share a row and the barcode runs full width below.
- Help `vouchers.md` step 6 in en/de/tr/ar/fa; de/ar section names corrected to match the UI label.

**Independent review** (Fable, read-only, ran build/vet/tests on print/data/pages)
1. MEDIUM — on-screen Code39 can't draw lowercase or >32-char codes (pre-printed cards, API-issued UUIDs). The till's own flow always generates 13-digit numbers, which render. Accepted for this card: help wording now says the on-screen barcode covers till-created numbers; CODE128 SVG filed as ut-docs#4248.
2. LOW — the em dash in the void notice prints as `?` on an `ascii` charset printer. Fixed: plain hyphen in every locale and pack.
3. LOW — a receipt with vouchers carries two CODE128 symbols (voucher codes, then the receipt number). Accepted: each has its own HRI and label; noted on the card.
Checked OK by the reviewer: `detail.ID` = `vouchers.issued_sale_id`; returns cannot issue vouchers; voided sales reprint with the notice; `Sprintf` takes the holder as an argument; clipping/wrapping at 42 columns; template escaping; tests fail on broken behaviour.

**Verified beyond unit tests**
- TDD: each test was run against the code without its fix and failed (repo method undefined; print block undefined; `buildReceiptDoc` with the wiring stashed → "voucher block = \"\" []"; template stashed → "holder line missing").
- `e2e/tests/gift-voucher-item-4214.spec.ts` (extended with a barcode count + geometry assertion) passed; screenshots looked at, 1280×720 and 360×740, English, light theme: number and value on one row, barcode full width under it, nothing clipped.
- The real `buildReceiptDoc` → `RenderText` output was read for a two-voucher sale (one void).
- Not checked: a physical thermal printer (no ESC/POS device in this session), RTL locales and dark theme on the receipt card.

Full `go test ./...`, gofmt, build, guard-data-access/i18n/help-topics/help-drift/compliance/core-neutral all green.

**Verdict:** safe to merge.
