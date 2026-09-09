# Self-order kiosk resolves tile images through item_images (ut-docs#1870)

**PR:** universaltill/universal-till (branch `fix/1870-self-order-kiosk-item-images`)
**Card:** universaltill/ut-docs#1870
**Complexity:** easy — Dev at Sonnet, Review at Sonnet (fresh context)

## What shipped

`internal/pages/self_order_shop.go`'s `loadShopItems` hardcoded every kiosk
tile's `ImageURL` to `"/public/assets/items/" + it.ID + "/thumb.png"` — the
upload-only path convention. Every other surface (sale screen, basket,
barcode/SKU search — `internal/data/pos_repo.go`) resolves an item's photo
through `item_images` instead, since a thumbnail can be either an uploaded
photo or a built-in category icon (the catalog image picker, ut-docs#1844,
or the automatic import placeholder, ut-docs#1189) — `item_images.path`
already holds the correct servable path either way. An item whose thumbnail
was a built-in icon showed a stale uploaded photo still sitting at the
guessed path, or a 404 hidden by the tile's own `onerror`.

- Added `CatalogRepo.ItemThumbnails(ctx) (map[string]string, error)` in
  `internal/data/catalog_repo.go`: one query
  (`SELECT item_id, path FROM item_images WHERE role = 'thumbnail'`)
  returning `item_id -> path` for every item, same batch shape as the
  existing `ItemBarcodes`/`ItemVariants`.
- `loadShopItems` now sets each tile's `ImageURL` from that map (empty
  string when the item has no thumbnail row) instead of reconstructing a
  path. `thumbnails, _ := repo.ItemThumbnails(ctx)` is deliberately
  best-effort: a nil map on error just means every tile falls back to
  no-image, same as a genuinely missing row.
- Three new tests in `internal/pages/self_order_shop_test.go`: a built-in
  icon shows correctly, an uploaded photo still shows (no regression), and
  an item with no thumbnail row gets an empty `src=""` rather than a
  guessed path that always 404s — the grid template's existing
  `onerror="this.style.display='none'"` already handles that gracefully.

## Independent review (Sonnet, fresh context, isolated worktree)

**Verdict: PASS — merged as-is**, plus one doc-comment nit fixed before
merge (see below). No blocker-class findings.

Verified empirically, not just by reading: ran `gofmt`, `go build ./...`,
`go vet`, `golangci-lint run` (0 issues), the full `TestSelfOrderShop*`
suite (29/29 green) and the existing `internal/data` thumbnail tests (8/8
green), plus `guard-data-access.sh`, `guard-i18n.sh` and
`guard-kiosk-engine.sh` (all green). Did a genuine revert-verify: reverted
`catalog_repo.go`/`self_order_shop.go` to `main`, reran the three new
tests, confirmed `TestSelfOrderShop_GridResolvesImageFromItemImages` and
`TestSelfOrderShop_GridItemWithNoThumbnailHasNoGuessedPath` fail with
exactly the pre-fix symptom (tile rendering the guessed upload path
instead of the seeded icon), confirmed `TestSelfOrderShop_GridStillShowsUploadedPhoto`
passes either way by design (it's the no-regression guard, not the bug
pin), then restored the fix and confirmed all tests green again.

Traced every `item_images` writer (`EnsureDefaultThumbnail`,
`SetItemThumbnail`) to confirm neither can produce more than one row per
`(item_id, role)`, so `ItemThumbnails`'s `out[id] = path` never silently
drops a row nondeterministically. Confirmed `setupSelfOrderShopDeps` runs
the real migrations (`internal/db/migrations/001_init.sql`), not a
parallel mock schema, so the new tests exercise the actual production
`item_images` table shape. Confirmed `imgv()` (`internal/httpx/httpx.go`)
passes an empty `ImageURL` through unchanged, and that a WHATWG-spec-
compliant `<img src="">` fires `error` immediately with no network
request in every render engine this app actually ships on (WebKitGTK on
Linux desktop per ADR-0028/`guard-webkit-version.sh`, Chromium `--kiosk`
on the Pi self-order kiosk, WebView2 on Windows) — so the tile's existing
`onerror` handler fires as intended, no re-request-current-page risk.

**Finding, fixed before merge:**

1. **Doc-comment inaccuracy**: `ItemThumbnails`'s comment said "every
   active item's current thumbnail path," but the query has no join to
   `items`/`is_active` — it returns thumbnails for inactive items too.
   Harmless today (the only caller, `loadShopItems`, only looks up IDs
   already filtered to active via `ListItems`), but the wording was wrong.
   Reworded to say the method doesn't filter on `is_active` and that a
   caller wanting only active items' thumbnails must filter its own ID set
   first (as `loadShopItems` already does).

**Findings noted, not acted on (out of this ticket's scope):**

- `internal/pages/ai_api.go`'s `/api/pos/identify` response builder
  (~line 157) has the same root bug class: it only ever resolves
  `ThumbURL` via `os.Stat` against the hardcoded upload-only
  `.../thumb.png` path, so an item whose thumbnail is a built-in icon or
  auto-import placeholder silently gets no `ThumbURL` in AI-identify
  results. Softer failure mode than the kiosk bug (guarded by `os.Stat`,
  so it degrades to "no thumbnail" rather than a stale/wrong image), but
  the same underlying gap. Filed as a new Backlog card
  (ut-docs#1875) rather than expanding this PR's scope.
- No dedicated `internal/data` unit test for `ItemThumbnails` in
  isolation (e.g. asserting the returned map directly for a small seeded
  set) — matches the existing convention for `ItemBarcodes`/`ItemVariants`,
  neither of which has one either, so not a new gap this diff introduces.

## Verified

- `gofmt -l` clean on all three changed files.
- `go build ./...`, `go vet ./internal/data/... ./internal/pages/...` — clean.
- `golangci-lint run ./internal/data/... ./internal/pages/...` — 0 issues.
- `go test $(go list ./... | grep -vE '/internal/plugins$|/internal/plugins/(oauth|marketplace)$')`
  (the exact command `ci.yml`'s `go test` step runs) — all green.
- `go test ./internal/pages/... -run TestSelfOrderShop -v` (29/29) and
  `go test ./internal/data/... -run Thumbnail -v` (8/8) — green.
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-kiosk-engine.sh`
  — all green.
