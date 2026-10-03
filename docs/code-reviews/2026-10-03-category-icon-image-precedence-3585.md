# Code review — category image and icon coexist, image wins (ut-docs#3585)

- **Date:** 2026-10-03
- **Branch:** `feat/3585-category-icon-image-precedence`
- **Author:** pipeline build lane (lane:cloud-41), Opus 5.5 subagent (card is `complexity:hard`)
- **Reviewer:** independent subagent, Fable (different model from the author); TDD and fixes re-verified personally by the orchestrator

## What shipped

A category's stored image (`categories.image_path`) and icon (`categories.icon`)
were mutually exclusive — setting one silently wiped the other. The till's
sale screen already showed "image when set, else icon" correctly
(`internal/iconid.Resolve`/`EffectiveIcon`, unchanged); the write paths did
not honor it. Categories-only scope per BA (items split to ut-docs#3601,
blocked on the in-flight ut-docs#3584; my.'s category tree/grid thumbnails
split to ut-docs#3602).

- `internal/data/catalog_repo.go` `SetCategoryPicture`: dropped the
  "both set → error" rule; both columns now write independently (icon
  format validation unchanged).
- `internal/pages/categories_page.go`: `storeCategoryPhoto` and
  `clearCategoryPicture` (the till's own upload dialog and the cloud's
  `set_catalog_image` directive) now read and preserve the category's
  current icon via a new `storedCategoryIcon` helper, instead of wiping
  it. The till dialog's own "No image" choice is a deliberate three-way
  exclusive pick (photo/none/icon) and keeps its old full-clear behavior
  via a new `clearCategoryPictureFully`; its icon-pick branch is
  byte-for-byte unchanged (still clears an uploaded photo, same as
  before — that exclusivity is this one dialog's own property, not the
  data layer's).
- `internal/data/catalog_save_repo.go` `SaveCategory` (the `save_category`
  directive, used when my.'s inspector patches the icon field): used to
  clear `image_path` for ANY non-empty icon. Now only clears a legacy
  library tile (`iconid.Resolve` says the path isn't a real photo);
  a real uploaded photo survives an icon change. `CategorySaveResult
  .ClearedImagePath` retired (it only ever covered the tile case, which
  has no file of its own to clean up).
- `internal/pages/cloudsync_wire.go`: categories report gains
  `icon_stored` (the raw column) alongside the existing `icon`
  (effective/display value), so my. can show the icon that's set even
  while an image displays.
- `e2e/`: new "photo" mode for the `category_picture` test tool, and a
  new Playwright spec proving a real uploaded photo survives a
  `save_category` icon directive on the sale screen.
- `web/help/{en,de,tr,ar,fa}/categories.md`: the shop-owner help topic's
  "a category has one picture, last writer wins" sentence was wrong for
  the cloud-icon-vs-till-photo case after this change — corrected in all
  five locales (`guard-help-drift.sh` confirms no new structural drift).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | Help topic (5 locales) described the old exclusive-replace behavior for the cloud-manage-page-icon-vs-till-photo case. | **Fixed** — rewritten in all five locales; the till dialog's own separate, still-exclusive built-in-image/photo sentence is untouched. |
| 2 | medium | `storedCategoryIcon` read only the `icon` column, so a pre-#2717 row (older till's library pick stored as `image_path`, `icon` NULL) lost its icon identity for good the moment a photo was uploaded over it — no column was left remembering the tile. | **Fixed** — falls back to `iconid.IDForAssetPath(cur.ImagePath)` when `icon` is empty; new test `TestCategoryImage_UploadOverLegacyTileKeepsTheTilesIcon`, confirmed red on revert, green restored. |
| 3 | low | Read-then-write of the icon column in `storeCategoryPhoto`/`clearCategoryPicture` isn't atomic — a `save_category` directive landing between the read and write could be silently overwritten. | Accepted, not merge-blocking: low likelihood (two writes to the same category racing within one tick), no money/data-loss shape, and fixing it means a new single-column repo method beyond this card's scope. |
| 4 | low (nit) | `ut-my-shop`'s new `.inspector__thumb` CSS hardcodes a shadow colour/radius. | Accepted — mirrors the pre-existing `.caticon` rule directly above it; not a new deviation. |
| 5 | low (nit) | e2e `photo` mode leaves a `thumb.png` on the worker till's disk; `afterEach` doesn't delete it. | Accepted — harmless, nothing asserts on its absence; a cleanup nit, not a correctness issue. |
| — | — | Two stale doc comments (`CategoryNode.ImagePath`, `CategoryPictureRow`) still asserted the old at-most-one-column invariant. | **Fixed** (wording only). |
| — | — | `manage_app.category.image_or_icon` i18n key (ut-cloud/ut-my-shop) is now unused. | Left in place — no unused-key guard in either repo makes this CI-blocking; noted for a future cleanup pass rather than touching 20+ locale files across two repos for this card. |

## Verification

- TDD re-verified personally (not just trusting the Dev/Tester/review subagent reports): reverted `storedCategoryIcon`'s fallback → `TestCategoryImage_UploadOverLegacyTileKeepsTheTilesIcon` failed with the exact predicted symptom (icon column blank after upload) → restored → green, `git status`/`git diff` confirmed byte-identical to the committed state afterward. The independent Fable review separately re-verified three more tests the same way (`TestSetCategoryPicture_ImageAndIconCoexist`, `TestSaveCategory_IconReplacesLibraryTileButKeepsPhoto`, and one in ut-my-shop) and the Tester phase re-verified a fourth (`e2e`'s new photo-survives-directive spec, reverting `catalog_save_repo.go`'s fix).
- `gofmt`, `go build ./...`, `go vet ./...` clean. Full `go test ./...`: all ~75 packages pass (Tester's independent run) plus the final re-run after this review's own two fixes (`internal/data`, `internal/pages` and subpackages, `internal/iconid` — all `ok`).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`: all pass, no new drift introduced.
- Playwright e2e: `category-icon-directive-2717.spec.ts` run for real, both the original tile-case test and the new photo-case test pass.
- Driven my. demo app (Tester + independent review, both separately): desktop 1280×900, phone 360×800, and fa-IR (RTL) all screenshotted and looked at — both Image and Icon sections visible, hint shown only when both are set, no overlap/clipping, RTL mirrors correctly. Functional check: removing an image from a category with both set leaves the icon showing, not blank.

## Verdict

Safe to merge.
