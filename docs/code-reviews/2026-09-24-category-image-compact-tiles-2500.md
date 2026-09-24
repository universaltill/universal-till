# Review: category image + compact category tiles (ut-docs#2500, slice 1)

- **Date:** 2026-09-24 · **Lane:** local · **Built by:** Opus 5.5 · **Reviewed by:** Fable (independent subagent, isolated worktree)
- **Branch:** `feat/2500-category-image`

## What shipped

Product owner, on the tablet (v0.21.3): "the category cards in the categories tab are too big, the categories still don't have icon or image".

- Migration `039_category_image.sql` (renumbered from 038: #2535 took 038 first): `categories.image_path`. It syncs with the existing whole-row admin sync; mixed-version fleets are tolerated.
- Category dialog (`categories.html` / `categories_page.go`) is multipart now. It adds an Image section: take a photo, choose a file, or pick a built-in icon or "No image". `ParseMultipartForm` falls back to `ParseForm` so the #2018 trap (multipart fields silently dropped) can't recur. Access stays manager-only and main-till-only.
- The decode, downscale and PNG steps are extracted to `internal/imaging/thumb.go`. The item and variant uploads use it too, and the write is now atomic: temp file + rename, after `MkdirAll`, under `paths.Data`.
- Resolver `categoryImageURL`: accepts only `/public/` paths without `..` whose file exists, so a satellite never renders a broken `<img>` for a photo that stayed on the main till.
- Sell screen: an image on the category tile, the strip tab and the overflow sheet. Tile `min-height` drops from 9.5rem to 3.5rem: 92px at 1280×800 (was ~170px), still ≥ 44px, and never taller than an item tile. The e2e checks this at 1024×600 and 360×800.
- i18n: 3 new keys in en/ar/fa/tr, with de/es pack PRs following. Help `categories.md` + `sell.md` updated in 5 languages; docs-shots regenerated.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | Dialog preview `data-image-url` was emitted even when the file isn't on this device, giving a broken preview on a satellite | **Fixed:** gated on `imgExists` |
| 2 | Low (worse than reported) | A 10–11 MB upload passed the 11 MB body cap but was truncated by the 10 MB read limit. A PNG with trailing bytes then decoded fine, so the category was **created** with a truncated read | **Fixed:** refuse `hdr.Size > 10 MB` with `image_too_large`. A new test case failed first (`code=200 rows=1`), then passed |
| 3 | Low | `ThumbErrorKey` sat between `removeUploadedThumbnail`'s doc comment and its func | **Fixed:** moved above |
| 4 | Note | Create path: a disk failure after the row insert reports an error while the row exists | Accepted: same ordering as the item handler, disk-failure only |
| 5 | Note | New en keys need de/es pack PRs | Done in the same cycle (`ut-plugin-language-{de,es}`) |

## Verified

- Reviewer re-ran the TDD claims in its own worktree:
  - Reverting to `ParseForm` → the create/edit and refusal tests fail with `code=400`, the #2018 trap.
  - Removing the migration → its test fails.
  - Both pass once restored.
- `go build`, `go vet`, `gofmt`, `golangci-lint` clean. Affected packages green. The local-only `mobile` LAN-dial failure is the same on `main`.
- e2e `category-image-2500.spec.ts` 3/3, plus related browsing-mode, overflow, dialog and editor specs (50 passed).
- Real app driven at 1280×800, 1024×600 and fa RTL; screenshots looked at: compact tiles with an uploaded photo, built-in icons and none; picker state on reopen.
- Guards: i18n, data-access, help-topics, help-drift, docs-shots, migration-collision.

## Safe to merge

Yes.

## Deferred (follow-up cards)

- Cloud: category image in the till→cloud snapshot and an `icon` on `update_category`; editor UI is #2526.
- Uploaded photos reaching satellites (items and categories).
- Kiosk category image (#1870 rule).
- Image editing from the Designer's category list.
- More built-in icons: #2506.
- A category showing only its quick buttons, not every item: #2541 (another lane).
