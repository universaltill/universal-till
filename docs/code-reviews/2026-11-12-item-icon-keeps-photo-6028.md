# Item icon keeps photo: universal-till part (ut-docs#6028 slice A)

Reviewer: independent subagent (Opus 5.5; code by Sonnet), 2026-11-12, on a
detached worktree of the WIP commit (`git diff origin/main...HEAD`).
Companion records: ut-cloud PR #979 and the contract PR ut-docs #6074, both
reviewed separately.

## What shipped

- `internal/data/catalog_save_repo.go` `saveItemIconTx`: save_item's icon
  now follows SaveCategory's #3585 rule. A non-empty icon keeps an uploaded
  photo: only `items.icon` is written, the thumbnail row stays, and
  `ClearedImagePath` stays `""`. A library tile (a named tile or the generic
  one, detected with `iconid.Resolve(thumb, icon)` returning path `""`) is
  still dropped and reported. Icon `""` is unchanged: the photo stays and the
  icon becomes NULL, and a named tile is cleared. The `ClearedImagePath` doc
  comment was updated to match.
- Cloud paths: the save_item directive (`cloudSaveItem`) and the mastered
  catalogue pull (`catalogpull` apply, then `ItemPhotoReplaced`) both reach
  `saveItemIconTx` through `SaveItem`. `removeReplacedItemUpload` is kept as
  a defensive check. Its guard `clearedPath == catalog.ItemThumbURL(id)`
  means a tile path is a no-op.
- `internal/cloudsync`: the snapshot item row gains `icon_stored` (raw
  `items.icon`, `""` for none), next to the displayed `icon`.
- Tests: new `item_picture_6028_test.go` (keeps photo, then falls back after
  the photo is removed; replaces named and generic tiles; changing the icon
  under a photo; Tester's clear regressions) and
  `catalogpull/item_icon_6028_test.go`. `TestSaveItem_Icon`,
  `TestCloudSaveItem_IconKeepsPhotoFile` and `TestSnapshotItemIcon` were
  updated to the new rule.
- Help: `web/help/en/catalog.md`, item image section.

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | Minor (doc) | The help said the icon "takes over once the photo is removed (from either side)". On the till, the only way to remove a photo is **No image** (`ClearItemPicture` → `ClearItemThumbnail`) or choosing a built-in image. Both clear the icon as well, as the same paragraph already says. Only my.'s "remove the image" (`ClearItemPhoto`) keeps the icon. | **Fixed**: "…once the photo is removed on my.universaltill.com (the till's own **No image** clears both)." |

Checked and clean:

- **Orphaned or wrongly deleted photo files**: no path deletes an uploaded
  photo's file any more. `ClearedImagePath` is now only ever a tile path.
  `removeReplacedItemUpload` removes a file only when the path equals the
  item's upload URL, and an icon no longer produces that path. The pull path
  (`apply.go:266`, then `ItemPhotoReplaced` in `catalogpull.go` and
  `push.go`) feeds the same function. No file is orphaned: the photo row is
  kept, so the file stays referenced.
- **LAN/replica**: `item_images` and icon choices are not in the LAN sync
  bundle (`adminTables` excludes them; see the comment on
  `POST /api/catalog/item/icon`). No other caller of `saveItemIconTx` exists
  (grep). The till's own icon picker (`SetItemThumbnail`, then
  `removeUploadedThumbnail`) is intentionally unchanged: on the till, a
  built-in image still replaces the photo, as the help says.
- **Fast path skips RowsAffected**: harmless, because `SaveItem` has already
  loaded the item in the same tx. Sell-screen cache invalidation still fires
  through the `items` UPDATE triggers (migrations 023 and 047). The display
  does not change anyway, since the photo wins.
- **Audit `Changed`**: `"icon"` is appended whenever `p.Icon != nil`, even
  on a replay with the same value. This is unchanged from base and matches
  SaveCategory. Accepted.
- **Generic placeholder kept on icon `""`**: the condition
  `IDForAssetPath(thumb) != ""` is byte-identical to base and to
  SaveCategory's. The generic tile has no icon id, so it is the importer's
  "no picture" placeholder and not the item's icon, and clearing an icon
  should not touch it. **Intended and accepted**; `TestSaveItemIcon_ClearRegressions`
  pins it. A non-empty icon does drop it, because `Resolve` treats every
  library path, generic included, as yielding to an icon. This matches
  categories.
- **Snapshot**: `icon_stored` is additive. The snapshot is content-hashed,
  so each till re-pushes once after upgrading. The ut-cloud side ingests the
  field on schema ≥ 2 (PR #979). `IconStored: it.Icon` reads the raw
  column (`catalog_snapshot_repo.go`), not the effective one. The test
  covers photo+icon → `lucide:beer` and a lone tile → `""`.
- Recurring bugs: the diff adds no file write (no `os.MkdirAll` concern) and
  no new filesystem path (no cwd-relative or `paths.Data` concern).
- Repository pattern: SQL only in `internal/data`. Tests with raw SQL are
  test files. No migration, i18n key or template change. Offline-first is
  unaffected.

## Extra verification (run by the reviewer in the worktree)

- `gofmt -l internal cmd`: no output. `go build ./...` ok. `go vet` on data,
  catalogpull, cloudsync and pages: ok.
- `go test` on `./internal/data/...`, `./internal/catalogpull/...`,
  `./internal/cloudsync/...`, `./internal/pages/...` and
  `./internal/iconid/...`: all ok.
- `golangci-lint run ./...`: 0 issues.
- Guards: help-topics, data-access, fixed-dates and i18n all pass
  (help-topics was re-run after the help fix).
- **TDD re-verified**: I restored base `catalog_save_repo.go` (the
  `saveItemIconTx` and comment hunks are the only diff in that file). Then
  `TestSaveItemIcon_KeepsUploadedPhoto` failed with
  `ClearedImagePath = "/public/assets/items/itm1/thumb.png", want none`.
  After restoring the file, it and `TestSaveItem_Icon` pass.
- Tester's driven run, re-read and attested: real binary, a fake cloud, and
  directives through the real tick → hooks → data path (see
  `tester-till.md` §3). Screenshots are under `scratchpad/6028/tester/run/`.
  `n2-after-icon-tile.png` shows the uploaded striped photo kept after
  `save_item {icon: lucide:beer}`. `n3-after-clear-tile.png` shows the
  purple beer icon after my.'s `set_catalog_image clear`. Both match the
  intent. The file sha did not change across the icon directive. The
  snapshot showed `icon:""`, `icon_stored:"lucide:beer"` and the photo's
  `image_sha256`.

## Verdict

**Approve** with the one help-prose fix applied (patch below). No blocking
findings.

Patch (`web/help/en/catalog.md`, one sentence):

```
-… and the icon takes over once the photo is removed (from either side). The picture …
+… and the icon takes over once the photo is removed on my.universaltill.com (the till's own **No image** clears both). The picture …
```

## Deferred

- ut-docs#6070, ut-docs#6071, ut-docs#6072: follow-ups from this card (out of
  slice A).
- Demo-seed bundled-thumb display (Tester observation, pre-existing, not
  caused by this change). Demo items whose id matches a bundled
  `web/public/assets/items/<id>/thumb.png` fall back to that embedded stock
  image after my. clears their photo, instead of their icon. The grid still
  emits the convention path, and the static handler serves it from embedded
  assets. The same happened before #6028 after an icon replaced the photo.
  Needs its own Backlog card (only demo-seed ids are affected).

Pre-flight: till user-visible behaviour → `web/help/en/catalog.md`
updated; `make docs-shots` n/a (prose only, no UI change); `reference/`
contract updated in ut-docs PR #6074; README n/a (no stale text). en.json
key n/a (none added). my. UI → ut-cloud PR #979 (reviewed separately).
Plugin/manifest mirrors n/a. Review record: this file. Follow-ups:
#6070/#6071/#6072 + demo-thumb card (to file).
