# Review: catalog.photos.v1 + photo-ordered identify references (ut-docs#4094)

## What shipped (ut-docs#4094)

AI camera identify ignored most reference photos. The AI plugin walked `catalog.items.v1` in name order and spent the host's 64 `item_image_open` calls per event (failed ones included) on the first 64 items. An item at position 183/230 with the exact photo never reached the model (the owner's case, SKU 30006-4).

- **universal-till:** new core read view `catalog.photos.v1` (`view:inventory`, offset/limit 1–500). It lists the active items that have a reference photo on disk as `{item_id, source: ai_ref|thumb, photo_at}`, sorted `ai_ref` first, then newest first, then id. `itemimages.PhotoStamp` does a stat only, with the id validated before any path join. Registry, pin test and README are updated.
- **ut-plugin-integration-ai 2.2.0:** `photoOrder` reads one page (limit 120) of the view, keeps the rows that are in the active catalogue and drops duplicates. The existing open loop then walks that order. Any view error (an older till's `-1`, a denial, bad JSON) falls back to the old name-order walk. `views_used` and README are updated.
- **ut-docs:** contract `plugin-views.md` 1.4.0, `plugin-host-functions.md`, `architecture/ai-plugin.md`.

## Review

Independent review by a different model (Fable; the code was written on Opus 5.5), read-only, with build, vet, tests and guards run. No blockers.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The universal-till README's list of core views omitted `catalog.photos.v1` | **Fixed** |
| 2 | minor | The view rebuilds the whole set per call (stat per active item) | **Accepted**: well inside the 5 s deadline, `ctx` checked per item, plugin reads one page |
| 3 | minor | The `photoViewLimit` comment claimed rows outside the catalogue "can't starve the set" | **Fixed**: comment reworded; the >500-item edge is noted (not a regression) |
| 4 | nit | Duplicate-row dedup and bad-JSON fallback were untested | **Fixed**: `TestIdentify_PhotoViewDuplicateRowsAndBadAnswer`; the dedup test fails with the dedup removed |
| 5 | nit | Contract wording on stat vs decode | No change: the next sentence states it |

## Verified beyond the automated tests

- TDD re-verified inline. With `PhotoStamp` broken, `TestPhotoStamp` and `TestCatalogPhotosView` fail. With `photoOrder` bypassed, `TestIdentify_ReferencePhotosFollowThePhotoView` (230 items, item 183 newest → its photo sent first, 60 refs, 60 opens) fails. Restored, all pass.
- `TestViewQueryCatalogPhotos` runs the view through a real wazero guest: the grant and `views_used` gate, a thumb on disk, and a denial without `view:inventory`.
- universal-till: `go build`, `go test ./...` green. One `internal/pages` failure during a concurrent run passed on rerun, and that package is untouched. `guard-data-access`, `guard-core-neutral` and `guard-i18n` pass. Local golangci-lint is too old for go1.27; CI runs it.
- Plugin: gofmt, vet (native and wasip1), tests, `build.sh`, `validate.sh` (v2.2.0), `guard-plugin-i18n`, shellcheck, `check-version-bump.test.sh`.
- No UI surface changed. No on-device run: the effect needs the till release carrying the view plus plugin 2.2.0 installed on the tablet. Until then the plugin behaves exactly as before.

## Verdict

Safe to merge. Merge order doesn't matter: the plugin falls back on tills without the view.
