# Review — gift voucher item type (ut-docs#4214, slice A)

**Shipped:**
- Migration 071 `items.item_type` (`standard` | `gift_voucher`, CHECK).
- Tapping or scanning a gift voucher item opens a popup: value, card number (hardware-scanner friendly, `data-scan-own`; blank → a generated 13-digit `99…` EAN-13 number), an optional "for" name, and any purchase / specific item.
- The popup refuses a duplicate number (vouchers table, main till, basket).
- The sale adds a voucher basket line: qty 1, NoMerge, never cached, no basket discount or service charge, qty/discount locked, carried through hold/resume.
- At checkout voucher lines become `IssueVouchers` entries: a liability for any purchase, taxed at the item's rate for a specific item. They are never sale lines, stock moves or kitchen tickets.
- Removing the line means the voucher is never issued.
- Scanning a voucher number without the `GS-` prefix offers it as payment (after item resolution, so a SKU/barcode always wins).
- The item editor has a Type select. The self-order kiosk hides gift voucher items.
- The Split tab's "Sell a voucher" block is deleted (UI, JS, 16 keys, `taxInclusive` data).
- Help `vouchers.md` rewritten and a `catalog.md` bullet added (en/de/tr/ar/fa); README updated.

**Built by:** Opus 5.5 subagent. **Reviewer:** independent Fable subagent (in a separate worktree).

**TDD re-verified by the reviewer (3/3 genuine):** with the line removed, each test fails, and it passes again once restored:
- voucher line not discounted (`service.go:983`) → `TestGiftVoucherLine_TotalsMultiPurpose` fails (discount 3.50, want 1.00);
- duplicate number → `TestScanAPI_GiftVoucherSubmit_DuplicateNumberRefused` fails;
- checkout → IssueVouchers → `TestTender_GiftVoucherOnlySaleIssuesVoucher` fails.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | High (pre-existing) | `buildFiscalSignPayload` ignores `VoucherIssues`: a voucher sale is signed with Total 0 and no band; the old Split-tab path was identical | Card **#4232** (p1, pinned next) |
| 2 | Medium (pre-existing, #1037) | Basket discount prorated into a specific-item voucher's VAT band (VAT off by 0.31 in the worked example) | Card **#4233** (p2) |
| 3 | Low | Popup bounded "for" by runes; tender validates bytes → long Arabic names failed checkout | **Fixed:** `boundGiftVoucherHolder` (bytes, rune boundary) + test, which failed first |
| 4 | Low | Dead `GiftVoucherItemIDs`, which had also split `SellScreenStates`' doc comment | **Fixed:** removed |
| 5 | Low | Stale "Sell a voucher" comment in `pos_api.go` | **Fixed** |
| 6 | Low | A generated number on a satellite is checked locally only (a clash is ~1e-10 and surfaces on replay) | Accepted |

**Gate:**
- `go build` / `go vet` clean.
- `go test -race`: 89 packages ok. `pages` (1605 s) and `db` (590 s) pass when run alone.
- `internal/plugins` hangs on `TestHTTPEgress/explicit_net:192.168.1.50` (it dials a real LAN address; untouched by this change). It passes with that case skipped.
- Guards: i18n, data-access, help-topics, help-drift and 7 others ok.
- e2e: 43 passed, including the new `gift-voucher-item-4214.spec.ts`: sell a voucher alone via the item, then spend part of it and check the remaining balance.
- `golangci-lint` not run locally (the binary is built with Go 1.26; CI runs it).

**Looked at:** the popup at 1024×600 (the first shot clipped Add/Cancel; the layout was fixed and a geometric test added) and at 360 px. **Not looked at:** dark theme, fa/ar RTL, German.

**Deferred:**
- de/es/pt language-pack PRs for the new keys (same cycle).
- #4224: barcode on the printed receipt.
- #4225: import ProductType=2 and a default item.
- #4230: cloud shop/menu hides gift vouchers.
- #4231: my. item type and `update_item_details`.

**Verdict:** safe to merge.
