# Review: item photo changed mid-sale reaches the open basket (ut-docs#4838)

Date: 2026-10-24 · Branch `fix/4838-basket-photo-mid-sale` · Built by Sonnet, reviewed by Opus 5.5 (fresh context)

## What shipped

universal-till#2114 fixed the quick tile, but the basket row could still miss
a photo. `pos.Service` keeps the `ImageURL` that was resolved when an item was
first rung up. It keeps it in two places: the live lines and the per-sale
`scanCache`. A repeat tap hits the cache, and `mergeResolved` never overwrites
with an empty URL. So when a photo was added, replaced or removed in the item
editor during an open sale, the open sale kept the old row until the next sale.

- `(*pos.Service).SetItemImage(itemID, imageURL)` updates every open line and
  cached scan of that item ("" clears it), and republishes the basket when a
  line changed.
- `catalog.RefreshBasketPhoto` reads the item's current `item_images`
  thumbnail and applies it to the cashier engine. It is called after:
  - the photo upload;
  - the built-in icon picker's set and clear;
  - my.'s `set_catalog_image` directive (items).

The self-order engines are deliberately left alone.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | No test exercised the scan-cache loop. Both tests started with no photo, so the cache never mattered. A photo removed mid-sale would come back on the next tap. | **Fixed**: added `TestSetItemImage_ClearsCachedPhoto_4838`. With the loop disabled it fails with `rescan after clear: qty=2 image="/old.png"`; it passes with the fix. |
| 2 | Low | Catalog import (`import_page.go` `SetItemThumbnail` / `EnsureDefaultThumbnail`) has no refresh call. | Accepted: a bulk import during an open sale that changes the photo of an item already in the basket is rare. The next sale shows the new photo. |
| 3 | Info | A held sale snapshots `ImageURL`, so a sale restored after a photo change shows the old photo. Self-order baskets and satellites are not refreshed. | Pre-existing and out of scope. Held sales have a follow-up card. |

The reviewer found no defect in:
- locking: `recomputeTotals` runs under `s.mu` and bumps `recomputeGen`;
- aliasing: published `Basket.Lines` is a copy;
- the `ErrItemThumbRow` path: the refresh reads the row that is actually stored;
- kiosk isolation and the repository pattern: both guards pass.

## Verified

- The reviewer reproduced each red result:
  - pages test without the handler calls: `open sale's row does not show the photo set mid-sale`;
  - without only the clear call: `removed photo still on the open sale's row`;
  - pos test with a no-op `SetItemImage`: `open line did not get the photo`.
- Every red case passes again once the fix is restored.
- `go build/vet ./...`, `go test ./...` and `go test -race ./internal/pos/` pass. `internal/plugins` timed out at 10m in the full parallel run; it is unrelated to this change and was re-run on its own.
- golangci-lint reports 0 issues. The CI guards pass, apart from two that can't run in this container: deadcode was built with an older Go, and shellcheck is missing. CI runs both.
- No browser run was done. The change is server-side state only; the rendered basket HTML is asserted through the real mux with the production resolver. No visible surface or template changed, so there were no screenshots and no help-topic change.

Verdict: safe to merge.
