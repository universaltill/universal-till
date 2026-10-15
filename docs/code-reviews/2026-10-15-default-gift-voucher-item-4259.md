# Review: default gift voucher item for new shops (ut-docs#4259)

Date: 2026-10-15 · Lane: `lane:cloud-24` · Built on Opus 5.5 and reviewed by Fable in a separate worktree, as a different model.

## What shipped
- `data.CatalogRepo.SeedDefaultGiftVoucherItem(ctx, name)` creates one active `gift_voucher` item: price 0, so the popup asks for the value; stock-untracked; no category; not sample data; generated SKU. The existence check and the insert run in one write transaction. It does nothing if any gift voucher item already exists, active or not.
- `POST /api/setup` (first-boot wizard) seeds it best-effort after the base-plugin install. The name is `catalog.item_type.gift_voucher` in the shop's locale. A failure is logged, and the wizard still finishes.
- Delete guard: a voucher sold through a gift voucher item is never a sale line (#4214), and voucher rows name no item. So both delete paths saw that item as "never sold". The fix:
  - `DeleteUnusedItem` (cloud `delete_item`) refuses with the new `ItemUseOpenVouchers` reason: "the shop has open gift vouchers — deactivate it instead".
  - `obsoleteItemsPredicate` (Catalog cleanup, and the DELETE inside `DeleteUnusedItem`) keeps any `gift_voucher` item while any voucher is `active`. The rule is shop-wide because vouchers carry no item id.
- Help: `vouchers.md` updated in en/de/tr/ar/fa, and `catalog.md` in en only (the cleanup bullet exists only in English, a pre-existing drift). ut-docs `reference/manage-shop-catalog-api.md` §3.10 is updated too.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Without the shop's language pack at seed time (offline, 5 s install timeout), the item is named in English ("Gift voucher"), because `T` falls back to en. The `name == key` guard only catches a key missing everywhere, but its comment claimed more. Help text over-promised. | The comment and the help (5 locales) now say exactly this. Renaming on pack arrival → **ut-docs#4398** (Backlog). |
| 2 | major | A `.bkp`/CSV import on the welcome page right after setup adds a second gift voucher item ("Gutscheine" next to "Gutschein"). | Product call with options → **ut-docs#4399** (Backlog, p2). |
| 3 | minor | The guard is shop-wide, so an unused duplicate voucher item is refused too while any voucher is active. | Accepted by design. The refusal text is accurate, and deactivating still works. |
| 4 | minor | "Also include active products that were never sold" removes the seeded item when the shop has no active vouchers. | Documented in en `catalog.md`, including how to re-add one. |
| 5 | minor | The de/tr/ar/fa `catalog.md` lack the cleanup bullet, so the new sentence has no counterpart there. | Pre-existing drift. Accepted. |
| 6 | nit | The GB test passes through the en fallback. | Covered by the AE (ar, bundled) case. The pack-arrival behaviour is #4398's test. |
| 7 | nit | en wording was clumsy. | Fixed. |
| 8 | nit | The bare `POST /api/auth/setup` fallback doesn't seed. | Accepted: it's a fallback for first boot without the wizard. |

The reviewer verified that `items.item_type` resolves unambiguously at all five places the predicate is spliced in. It also confirmed that the voucher status vocabulary has no other open state, that satellite-issued vouchers replay to the primary (where the guard runs), and that the join snapshot and backup restore give no duplicates.

## Verified
- TDD: the data tests failed against stubs, which confirms that cleanup previewed and removed a voucher item while vouchers were open, and that delete accepted it. The wizard test failed before it was wired. All of them pass now.
- `gofmt`, `go build`, `go vet` and `golangci-lint` (0 issues) are clean. `go test ./...` is green: `internal/plugins` timed out in the loaded full run and passed alone (880 s), and the reviewer ran the full `internal/pages` package independently. The CI `build` job guards pass, except two that can't run in this container: deadcode (its binary is go1.26 against a go1.27 module) and shellcheck-version (no shellcheck installed).
- Driven run: a fresh binary went through the wizard via `POST /api/setup` for GB (en) and AE (ar, RTL) at 1024×600 in light theme. The sale screen shows "Uncategorized · 1 item(s)", and tapping the item opens the Gift voucher popup (value 0.00, card number focused). Catalog lists the item at 0.00. There were no console errors. **Not looked at:** dark theme, phone width, de/fa.

## Verdict
Safe to merge. The deferred items are #4398 and #4399.
