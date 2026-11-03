# Review: AI catalog tools, core seam (ut-docs#4128)

Branch `feat/4128-ai-catalog-tools`. Author: Sonnet 5.5 build; independent
review by Opus 5.5 (read-only, ran the suites). Part of epic #2542; image
find/edit is explicitly out of this slice (needs an owner decision on the
image source and licence).

## What shipped
- `catalog.item.update.v1` (internal/agentaction): name, price, category,
  and a barcode for an item that has none. Before/after rows from the DB,
  stale guard re-read inside the transaction, written through
  `UpdateItemTx` / `AddNormalizedBarcodeTx` (the item editor's own path, so
  price history and sync match), role = `catalog_management`, audited by
  the existing `agent.action.*` gate, satellite tills refused by the
  existing propose gate. Never reachable: sale, fiscal, payments.
- `catalog.gaps.v1` view (view:inventory, class catalog): active items
  missing barcode / own photo / price / category, bitmask, paged, capped.
  The catalog page's `?missing=<mask>` filter uses the same query.
- `item_list` view component: ids (+ gap mask) only; core draws name,
  thumbnail, edit link and a filtered-catalog link, only for roles with
  `catalog_management`. A plugin supplies no text, image or URL.
- Locale keys (en, tr, fa, ar; de lives in the language packs), key
  metadata, help topic in en/de/tr/ar/fa, help-drift baseline (lockstep
  +1/+1), plugin guidelines.

## Findings (reviewer) and disposition
- BLOCKER plugin suite red (view map) - fixed (plugin PR).
- MAJOR stale guard ignored "item gained a barcode meanwhile" - fixed with
  `ItemBarcodeCountTx`; test fails without the fix (verified by mutation).
- MAJOR registry closed-set check weakened - replaced by a rule test (area
  allow-list, prefix, forbidden words) that holds for sibling cards.
- MAJOR mixed find/gap list gave a misleading catalog link - fixed in the
  plugin (link dropped when the list is mixed).
- MAJOR plugin min_pos_version - set to 0.36.0 in the plugin PR; owner must
  confirm it equals the core release that ships this.
- Minor fixed: repeated barcode in one batch refused at parse; tr typo;
  dark-mode thumb fallback colour; guidelines sentence order; changelog order.
- Minor accepted / follow-up cards: barcode normalised only at confirm;
  category deactivated or deleted between propose and confirm (matches the
  manual editor; a deleted category fails as "failed"); gaps view ignores
  variants (false positives); `find_items` can spend the 25-call budget;
  audit.summary needs user_management on an `ai.agent` plugin.

## Verified beyond unit tests
Mutations: removing the name-stale compare, the barcode-stale check and the
catalog_management gate on item_list each fail a test. Full `go test ./...`
green; guards: i18n, i18n-meta, data-access, core-neutral, rtl, help-topics,
help-drift, compliance, competitor. NOT verified: a real till device, a
screenshot of the confirm card / item list / catalog banner (no browser
drive in this run), RTL and 360px layout by eye.

## Verdict
Safe to merge after CI, together with the ut-docs contract PR, and BEFORE
the plugin PR. Language packs: no pack carries these core keys yet (new
keys; `lang-pack-drift` on main expected red until the pack PRs land).
