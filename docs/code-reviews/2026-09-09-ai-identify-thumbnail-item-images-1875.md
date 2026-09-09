# AI camera-identify resolves ThumbURL through item_images (ut-docs#1875)

**PR:** universaltill/universal-till (branch `fix/1875-ai-identify-builtin-icon-thumbnail`)
**Card:** universaltill/ut-docs#1875
**Complexity:** easy — Dev at Sonnet, Review at Sonnet (fresh context)

## What shipped

`internal/pages/ai_api.go`'s `POST /api/pos/identify` handler resolved each
match's `ThumbURL` via `os.Stat(itemAssetDir()/<itemID>/thumb.png)` — the
same upload-only path assumption ut-docs#1870 fixed for the self-order
kiosk grid. A built-in category icon or automatic import placeholder
(recorded in `item_images` via `data.CatalogRepo`, not on disk at that
guessed path) never resolved, silently degrading to no `ThumbURL` for that
match (softer than #1870's stale-image failure, since the field is
`omitempty`, but the same root gap).

- Replaced the per-match `os.Stat` check with one batch
  `catRepo.ItemThumbnails(r.Context())` lookup fetched before the match
  loop, then `out.ThumbURL = thumbnails[m.ItemID]` — identical pattern to
  `self_order_shop.go:52` and `catalog/row_oob.go:286`.
- `catRepo` was already constructed in `registerAIAPI`; no new repository
  instance needed. `os`/`filepath` remain used elsewhere in the file (the
  `/confirm` handler's `MkdirAll`/re-encode path), so no import cleanup.
- Updated `TestIdentifyAPI_SuccessReturnsMatchesWithPriceAndThumbAndAudits`
  to seed the uploaded-photo case via `CatalogRepo.SetItemThumbnail`
  instead of writing a file to disk (the field is no longer resolved from
  disk at all).
- Added `TestIdentifyAPI_ThumbURLResolvesBuiltInCategoryIcon` (the primary
  regression — a built-in icon path with no file on disk resolves
  correctly) and `TestIdentifyAPI_ThumbURLEmptyWhenNoThumbnailSet` (no
  `item_images` row → empty `ThumbURL`, no error).

## Independent review (Sonnet, fresh context)

**Verdict: SAFE TO MERGE.** No blocker-class or other findings in the code.

Verified empirically, not on trust: confirmed `self_order_shop.go`/
`catalog/row_oob.go` actually use the same `ItemThumbnails` pattern cited
as precedent. Did a genuine revert-verify — stashed only `ai_api.go`, ran
the three touched tests against the old code, confirmed
`TestIdentifyAPI_SuccessReturnsMatchesWithPriceAndThumbAndAudits` and
`TestIdentifyAPI_ThumbURLResolvesBuiltInCategoryIcon` fail with the exact
expected messages (`TestIdentifyAPI_ThumbURLEmptyWhenNoThumbnailSet` passes
either way by construction — a valid acceptance-criterion test, not a
regression pin, since `os.Stat` on a nonexistent file also yields empty
`ThumbURL`), restored the fix, confirmed all three pass again. Confirmed
`os`/`filepath` are still genuinely used elsewhere in the file. Confirmed
the test file's `data.NewCatalogRepo(db).SetItemThumbnail(...)` seeding
goes through the repository, not raw SQL (`guard-data-access.sh` green).
Confirmed a nil map from a failed `ItemThumbnails` call is a safe Go map
read (zero value, no panic), matching the best-effort comment's intent —
same failure-degrades-to-no-thumbnail shape as an item with no row at all.

**Findings:** none.

## Verified

- `gofmt -l` clean on both changed files.
- `go build ./...` — clean.
- `go vet ./internal/pages/...` — clean.
- `golangci-lint run ./internal/pages/...` and `golangci-lint run ./...`
  — 0 issues.
- `go test ./internal/pages/... -run 'TestIdentifyAPI|TestConfirmAPI|TestLoadReferenceImages|TestLoadRefJPEG|TestPruneAIRefs' -v`
  — all green, including the two new tests.
- `go test ./...` (full repo suite) — all green, no regressions.
- `bash scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-page-http-error.sh` — all green (no i18n/UI
  surface touched by this change, so these pass trivially).
