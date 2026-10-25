# Review: a recalled held sale shows each item's current photo (ut-docs#4958)

Date: 2026-10-25 · Branch `fix/4958-held-sale-photo` · Built by Sonnet, reviewed by Opus 5.5 (fresh context)

## What shipped

A held sale snapshots every line's `ImageURL` (`internal/pos/hold.go`). If the
item's photo was replaced or removed while the sale was parked, resuming it
brought the old photo back. Once the old upload file was gone, the row showed
a broken image. #4838 refreshed the open sale only.

- `resumeHeldSale` (`internal/pages/hold_api.go`) now calls
  `refreshHeldLinePhotos` just before `RestoreHeld`. For every line with an
  `ItemID`, it sets `ImageURL` to the item's current `item_images` thumbnail,
  or `""` when the item has none. It follows the same rule as the
  ut-docs#820 table label: catalogue-derived display data is re-resolved,
  never trusted from the payload.
- `(*data.CatalogRepo).ItemThumbnailsFor(ctx, ids)` does this in one batch
  query. Empty input runs no query.
- The refresh is best-effort. A read error is logged, the snapshot values
  stay, and the resume still succeeds (offline-first).
- Item photos are per-till local files. Reading this till's own
  `item_images` is therefore also right for an order parked on another till.
- `POST /api/pos/resume` and `/open-orders` both go through
  `resumeHeldSale`, so the fix covers both. The self-order kiosk engine is
  untouched.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | The refresh ran before the "already on this order" no-op return, so that case did one query and threw the result away. | **Fixed**: moved to just before `RestoreHeld`. |
| 2 | Low | No test covered the read-error path, which is what keeps the resume safe. | **Fixed**: added a "photo read fails" case. It drops `item_images` before the resume and expects a 200 with the snapshot photo kept. |
| 3 | Nit | The pages test creates `items`/`item_images` by hand instead of using a migrated DB. | Accepted. This matches `newHoldTestDeps`, which the whole hold suite uses. The repo method has its own test on a migrated DB. |

The reviewer checked every ring-up path and found no line kind whose photo
comes from anywhere other than the item's own thumbnail. That covers scan
tiers, variants (they use the parent's thumbnail), shortcuts, the by-id
lookup, gift voucher, open-price, custom lines and kiosk pay-at-counter
orders. By-hand lines are stored apart from priced lines. The basket shows
only `ImageURL`, with no icon fallback, so a cleared photo matches what a
fresh ring-up would show. The IN list is one entry per distinct item, far
below SQLite's 32766-variable limit.

## Verified

- TDD claim checked by the orchestrator: with the call removed, the pages
  test fails as follows. With it restored, it passes.
  - replaced: `restored line ImageURL = ".../old-thumb.png", want ".../new-thumb.png"`
  - removed: `... want ""`
- The reviewer repeated the same check in a throwaway copy, with the same result.
- `gofmt`, `go build ./...`, `go vet`, `go test ./...`, `golangci-lint`: see
  the PR for the gate. The `ci.yml` build-job guards were run locally.
- No browser run was done. The change is server-side engine state only: no
  template, CSS or route changed, so there are no screenshots and no
  help-topic change.

Verdict: safe to merge.
