# Review — item photo missing on sell-screen quick tile (ut-docs#4838)

**Date:** 2026-10-23 · **Built by:** Sonnet 5.5 · **Independent review:** Opus 5.5 (2026-10-23)

## What shipped
- Root cause: `ShortcutsRepo.loadButtons` used `COALESCE(sb.image_path, <item_images thumbnail>)`, so any non-NULL `shortcut_buttons.image_path` (a frozen library/placeholder tile saved by the designer) beat the photo set in the item editor. The editor writes only `item_images`, so the photo showed there and nowhere on the quick tile.
- Fix: the item's thumbnail now wins; the button's own image is only the fallback for an item with no photo row. No layout or template change, no new i18n keys.
- Test: `internal/ui/item_photo_sell_screen_4838_test.go` (failed first with the frozen placeholder still shown).
- Basket rows resolve the image from `item_images` in every resolver tier, so a photo already reaches them; checked by reading, not by a new test.

## Findings
| # | Finding | Handling |
|---|---|---|
| 1 | Quick-tile root cause confirmed: Designer search-result add posts imageUrl (internal/ui/buttons.go AddVals) so a placeholder is frozen into shortcut_buttons.image_path; later photos only update item_images | fixed (item photo wins) |
| 2 | Basket path has no server-side defect (4 resolver paths tested: barcode, SKU, explicit tile with placeholder, implicit item: tile) | regression test item_photo_basket_4838_test.go added; owner must say device/steps |
| 3 | Photo should beat a custom tile image (no UI lets anyone pick a tile image) | accepted; TestLoadButtons_FallsBackToCatalogImage updated |
| 4 | Basket may still look photo-less: .line-thumb hidden <=480px (#1314); scanCache keeps a photo-less line for the rest of a sale; icon-only items have no basket fallback | follow-up card |
| 5 | AddVals should stop sending imageUrl so new frozen images are not created | follow-up card |

## Verified
Reviewer ran gofmt, go build, go vet, go test ./internal/data ./internal/ui ./internal/pos and a filtered ./internal/pages subset; all pass. Old COALESCE reproduces the failure.

## Not verified
Real tablet/kiosk/satellite rendering; satellites show a photo only where the file synced (#2566).

## Safe to merge
Yes for the quick-tile half. Does not by itself close the owner's basket complaint (see findings 2 and 4).
