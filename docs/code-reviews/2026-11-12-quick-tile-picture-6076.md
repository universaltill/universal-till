# Review: quick tiles follow the item's picture, not a frozen shortcut path (ut-docs#6076)

**Date:** 2026-11-12 · **Branch:** `fix/6076-quick-tile-picture` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (Dev) · **Reviewer:** Opus 5.5 (independent subagent, separate worktree)

## What shipped

`ShortcutsRepo.loadButtons` (`internal/data/shortcuts_repo.go`) used
`COALESCE(item_images thumbnail, shortcut_buttons.image_path)`. The demo seed
writes the bundled `/public/assets/items/<id>/thumb.png` into
`shortcut_buttons.image_path` for itm001–itm010. Both clears (the cloud
`set_catalog_image` clear → `ClearItemPhoto`, and the till's **No image** →
`ClearItemThumbnail`) delete only the `item_images` row. So the tile fell back
to the frozen bundled path, and `iconid.Picture` treated it as a photo that
beats the item's icon.

The query now reads only the `item_images` thumbnail. That is the same source
`POSRepo.resolveShortcut` and the basket, search, kiosk and catalogue list
already use. `ButtonStore.LoadWith` then applies `iconid.Picture(thumb, icon)`.
The column is kept, because migrations are append-only and LAN sync copies it,
but nothing reads it. Comments in `shortcuts_repo.go` and `buttons.go` are
updated, and `docs/data-model.md` marks `image_path` as legacy.

## Tests

- New `TestLoadButtons_ImageOnlyFromItemThumbnail_6076` (LoadButtons and
  LoadGridButtons; a frozen bundled path and a frozen library path both give
  `""`).
- New `internal/ui/item_photo_sell_screen_6076_test.go`, which runs on the
  real demo seed:
  - a cloud clear gives the `lucide:beer` asset;
  - **No image** gives `""`;
  - a never-cleared item keeps its bundled photo;
  - after upload and then a cloud clear, the tile shows the icon.
- These existing tests are updated because they pinned the old fallback:
  - `TestLoadButtons_FallsBackToCatalogImage`
  - `TestSaveButtons_ReplacesWholeSetInOrder`
  - `TestButtonsHTTPAdd_NormalizesImageAndRendersGrid`, which now reads
    `image_path` straight from the DB
  - the Tea Tile case in `buttons_store_test.go`
- **TDD re-verified twice.** The Tester and the Reviewer, each in their own
  checkout, restored `shortcuts_repo.go` from `origin/main`. The new tests then
  failed with the frozen `thumb.png` / `beer.svg` path, and passed again with
  the fix restored.

## Extra verification (Tester, driven run)

The Tester ran a real till on the demo seed, with itm001 set to icon
`lucide:beer`, and drove it with Playwright. The tile was checked in rest view,
edit mode and at 390 px phone width.

- **Before the clear:** the tile shows `itm001/thumb.png`.
- **After the production `catalog.ClearItemPhoto`:** the tile `img` src is
  `beer.svg`, and so is a new basket line. The pre-fix binary on a copy of the
  same database still showed `thumb.png`.
- **After clicking No image:** the tile is plain, with no `img` and no
  `thumb.png` request. Items that were never cleared keep their stock photos.

Visual attestation: tablet and phone screenshots, light theme, English.

Gates:
- Full `go test ./...` is green; `internal/plugins` needs CI's 20m timeout.
- gofmt, vet and golangci-lint report 0 issues.
- The build-job guards pass, except two that cannot run in the container: the
  catimport tags are absent and shellcheck is not installed. This change
  touches neither.
- 78 Playwright e2e specs pass (designer, sale tile, phone, icon).

## Findings

| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | Low | `POST /api/buttons/add` still accepts and stores `imageUrl`, which is now never shown. | Deferred → ut-docs#6097. The add API, `SaveButtons`, `AddButton` and the demo seed should stop writing `image_path`. The Designer's own Add posts only the item's existing thumbnail, so no operator choice is lost today. |
| 2 | Low | An installed till whose tile picture lived only in `image_path` (an older Designer froze library tiles there, #4838), with no `item_images` row and no `items.icon`, now shows the plain tile. | **Accepted.** This matches every other surface for that item: basket, search, kiosk and catalogue already showed nothing. No data is deleted. There are no real shops yet. A migration copying the path into `items.icon` would also bring back pictures operators removed with **No image**. |
| 3 | Nit | Raw SQL in `buttons_http_test.go`. | Accepted: test-only; `guard-data-access.sh` passes. |

Recurring-bug checks (no `os.MkdirAll`-less writes, no cwd-relative paths): clean.

**Pre-flight:**
- Till user-visible behaviour → `web/help/en/catalog.md`: n/a. It already says
  No image clears to blank and a cloud removal shows the icon again; confirmed
  against the driven run.
- docs-shots: n/a. No help screenshot shows a cleared demo tile.
- reference: updated (`docs/data-model.md`).
- README: n/a.
- `en.json`: n/a (no keys).
- ADR: n/a (none covers tile pictures).
- Follow-up: ut-docs#6097.

## Verdict

Approve. Finding 1 is deferred to a card and finding 2 is accepted with the
reasons above.
