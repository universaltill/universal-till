# Review: an import retires the setup-seeded gift voucher item (ut-docs#4399)

- **Date:** 2026-10-15
- **Lane:** `lane:cloud-41` (author: Opus 5.5)
- **Reviewer:** Sonnet 5.5, independent subagent. The Fable review MODEL-ROUTING asks for on a medium card was refused: the account was out of Fable usage.
- **Scope:** `internal/data/gift_voucher_item.go`, `internal/data/item_delete_repo.go`, `internal/data/sync_admin_repo.go`, `internal/pages/import_page.go`, tests, `web/locales/{en,ar,fa,tr}.json`, `web/help/*/vouchers.md`, docs-shots manifest.

## What shipped

Setup seeds a gift voucher item (#4259) and lands on `/import?welcome=1`. A `.bkp`, CSV or XLSX import can bring the shop's own voucher item, which left two voucher tiles on day one. This change builds the card's recommended option 1:

- The seeder records the item's id and name in two shop-wide settings, written in the same transaction as the item.
- An `/import` commit that created a gift voucher item checks the live cashier and kiosk baskets, then calls `POSRepo.RetireSeededGiftVoucherItem`. That call deletes the seeded item only through `DeleteUnusedItem`'s rules (never sold, no stock history, not on a quick button, not in a parked sale, no active voucher). It also requires another active voucher item to exist and the seeded item to be untouched: same name, no price, category, tax code, colour or icon, and still active and on the sale screen. The two extra checks run as a precondition inside the delete's own write transaction.
- Once a decision is made (removed, already gone, edited by the shop, or in use), the marker is cleared, so no later import removes an item the shop has made its own. If no other voucher item exists, or the seeded item sits in a live basket, the marker stays for the next import.
- The commit summary gains a translated line naming the removed item. The import audit row records `seeded_gift_voucher_item_removed` and the item id. The manual (`vouchers.md`, all five locales) says so.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A seeded item the shop had renamed or priced, but not yet sold, would still be deleted by a later import. | **Fixed:** retire only an untouched item. Edited means kept, and the marker is cleared. Tests `renamed` and `priced` fail without the check (verified). |
| 2 | major | The "another active voucher item" check ran outside the delete transaction, so a concurrent deactivation could leave zero voucher items. | **Fixed:** `deleteUnusedItem` takes a precondition that runs inside its transaction. Both checks moved there. |
| 3 | minor | The marker's lifetime is unbounded while no other voucher item exists. | Accepted: with finding 1 fixed, it can only remove an untouched, unused seeded item. |
| 4 | minor | The audit trail held only a bool. | **Fixed:** the audit payload carries the item id, and the summary line carries the name. |
| 5 | minor | A basket-held or failed retire isn't shown to the operator. | Accepted: it's best-effort, both items stay, and the next import retries. |
| 6/7 | minor | Test gaps (basket retry, `.bkp`, replica). | Partly covered: a page test asserts the basket-held case keeps the marker. `.bkp` shares the handler and the `created++` site. Replicas get the settings as shop-wide keys, and the item delete replicates like any admin change. |
| 8 | nit | Arabic uses "صنف" in the locale string and "منتج" in the help. | Accepted: each matches its own file's existing wording. |
| 9/10 | nit | An empty marker value; a `%` in a future translation. | Comment added at the format call. |

The full suite also caught the new keys missing from the setting-scope classification (`TestSettingScope_EveryUsedKeyIsClassified`). They are now classified shop-wide in `ShopWideSettingPrefixes`.

## Verified beyond unit tests

- **TDD:** the data tests failed to compile before the repo methods existed. `TestImport_GiftVoucherRowRetiresTheSeededItem` fails with the page change stashed. The basket test fails with the basket check disabled. The `renamed` and `priced` cases fail with the untouched check disabled.
- **Driven run:** a fresh till built from this branch, the wizard via `POST /api/setup`, then a CSV with one `gift_voucher` row imported through the real `/import` form, including the currency-confirm step, in Chromium.
  - The summary showed "The gift voucher item “Gift voucher” created at setup was removed, because this import brought its own."
  - The sale screen listed only the Coffee and Gutscheine categories, with no uncategorised second voucher tile.
  - Looked at: en, 1280×800, light theme. Not looked at: RTL and phone width. It is one plain `<p>` inside the existing summary block.
- **Guards:** every guard in `ci.yml` run locally; `guard-price-history-sync.sh`'s allow-list now names `deleteUnusedItem`, where the price_history delete moved. deadcode and shellcheck-version could not run in this container (toolchain mismatch / no shellcheck); CI runs them. Also i18n, data-access, core-neutral, help-drift, help-topics and docs-shots all pass. `make docs-shots` was run; only the vouchers topic hashes changed. Its reports/invoices PNG churn is the known font drift (ut-docs#4407) and was not committed.

## Verdict

Safe to merge. A new `en.json` key means follow-up PRs in ut-plugin-language-de/es/pt in the same cycle.
