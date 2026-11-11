# Review: an item keeps its icon when a photo is written (ut-docs#3601)

- **Card:** ut-docs#3601 extends #3585's category rule to items: the photo shows if there is one, otherwise the icon, and both are stored. It was cut to the till-local half. The my.-driven half (`save_item` icon, snapshot `icon_stored`, ut-cloud, the my. inspector) is ut-docs#6028.
- **Branch:** `fix/3601-item-photo-keeps-icon-v2` (`fix/3601-item-photo-keeps-icon` holds only the pre-review WIP snapshot); ut-docs `docs/3601-item-photo-keeps-icon` (§0.12 of `reference/manage-shop-catalog-api.md`).
- **Pipeline:**
  - BA and Tester: Opus, separate agents.
  - Architect: Opus.
  - Dev: Sonnet.
  - Reviewer: Opus, a separate agent with a fresh context.
  - UX: skipped, because no template, CSS or string changed.
  - Unattended cloud cycle on `lane:cloud-41`.

Pre-flight:
- web/help topic: updated (`web/help/en/catalog.md`). `make docs-shots` was re-run and no screenshot changed. The re-taken channel-orders/reports PNGs were byte noise and were restored from `main`; only `manifest.json` was refreshed.
- `reference/`: updated (ut-docs §0.12).
- README: n/a.
- en.json/meta: n/a, no new keys.
- my./ut-cloud locales: n/a, deferred → ut-docs#6028.
- ADR: n/a, this applies the decision #3585 already made.
- Mirrors/manifest: n/a.
- Picker "No image" and built-in pick: deliberately unchanged, deferred → ut-docs#4846 (awaiting a decision).
- Review record: this file.

## What shipped
- `internal/data/catalog_repo.go`:
  - `SetItemPicture` no longer refuses a photo plus an icon.
  - New `SetItemPhoto` and `ClearItemPhoto` read and write in one transaction. They keep `items.icon`, and carry a lone library tile's id into it, mirroring `storedCategoryIcon`.
  - `ClearItemPhoto` deletes the file only after commit.
- Photo writers now keep the icon:
  - `catalog.StoreItemPhoto` (till upload and cloud `set_catalog_image`)
  - barcode-lookup photo (`internal/pages/catalog/handlers.go`)
  - import photo (`internal/pages/import_page.go`)
- The cloud `set_catalog_image` clear uses the new `catalog.ClearItemPhoto` and keeps the icon. The till picker's "No image" (`ClearItemPicture`) and its built-in pick (`SetItemThumbnail`) behave as before.
- `cloudSetCatalogImage` decides a replay on the stored image alone. With an icon kept under the photo, a replay no longer adds a second audit row; this fixes categories too.
- Help wording updated. It also fixes a stale claim that self-order ignores built-in images.
- Tests:
  - New: `internal/data/item_picture_3601_test.go`, `internal/pages/item_picture_3601_test.go`, `internal/pages/catalog/item_photo_3601_test.go`.
  - Updated, not deleted: the #3584 XOR tests, the category set/clear test and the snapshot test.

## Findings (independent Opus review) and resolution
| # | Sev | Finding | Resolution |
|---|---|---|---|
| 1 | low | Barcode-lookup and import photo paths have no test; each is a one-line swap | Accepted. The data-layer and upload tests cover `SetItemPhoto` |
| 2 | low | A pulled or `save_item` `icon:""` clears an icon kept under a photo, so removing the photo later leaves the item blank | `save_item` semantics, which belong to ut-docs#6028; noted there in a comment as an AC |
| 3 | info | LAN replicas, which don't sync `item_images`, now draw the kept icon instead of nothing | Intended `iconid.Resolve` fallback |
| 4 | info | A cloud clear on an icon-only category no longer writes a no-op audit row | Correct; no category test for it (optional) |
| 5 | info | my. "remove image" on a lone library tile turns it into its icon id, and the picture shown stays the same | Same as the category precedent |

## Verification
- The Reviewer re-ran TDD claims by reverting each fix:
  - `StoreItemPhoto` back to `SetItemThumbnail` made `TestItemImageUpload_KeepsStoredIcon` fail.
  - Dropping the tile carry-over made `TestSetItemPhoto_KeepsIcon/tile_only_+_photo` and `/clear_tile_only` fail.
  - Both passed again after restoring.
- The Tester broke the code four more ways and each was caught: the kept icon, the cloud clear, the replay concat, and the XOR refusal.
- `go build ./...`, `go vet ./internal/...`, gofmt and `golangci-lint` are clean. Tests pass for data, pages, cloudsync, catalogpull, ui and iconid.
- Every `ci.yml` build-job guard passed locally, except `guard-shellcheck-version` (no shellcheck binary in this container; CI has it).
- The only `-race` failure under machine load was a timing threshold in `TestStockUpload…`, unrelated to this change and passing without `-race`.
- Driven run on the demo seed:
  - Beer tile, then photo upload: `items.icon=lucide:beer` was kept and the sale tile showed the photo.
  - Cloud clear: the icon was kept and the file deleted. With the shortcut's own picture blank, the tile fell back to the beer icon.
  - Till "No image" still cleared both.
  - The Tester looked at the screenshots (1280×800, light, en).
- Pre-existing, not a regression: a quick button with its own picture keeps showing that picture after a photo clear (`shortcuts_repo.go:96`).

## Verdict
Approve.
