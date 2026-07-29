# Stale image cache after re-upload; inventory search filter resets on save

2026-07-29

## Reported by Farshid (live testing)

> "inventory is working but when I save, it refresh the page an even I have
> the latte in the filter it shows all the lists. catalog image and variant
> image is not working."

This followed the previous session's fix for uploaded images being wiped on
every app update (PR #81) — the write path was moved from the cwd-relative
release tree to the stable per-user data dir (`internal/paths`). Farshid
tested v0.2.49 live and still saw broken images, plus a new complaint about
the inventory search filter.

## Bug 1: stale image cache after re-upload

**Root cause**: `assetVersion` (`internal/httpx/httpx.go`) computes the
`?v=<mtime>` cache-busting query param appended to every `/public/...` URL by
`imgv`. It only ever `os.Stat`'d the old cwd-relative `web/<rel>` path. Once
PR #81 moved uploaded item/variant photos and the receipt logo to
`paths.Data(...)`, that stat always missed for an uploaded file — the
function silently fell back to `bootTime`, a constant fixed at process start.

Effect: the very first upload of a given item's photo works fine (the URL is
new to the browser). But re-uploading — editing an existing item's photo, or
a variant's — keeps the exact same `?v=<bootTime>` query string, so the
browser serves the stale cached bytes and the new photo never appears to
show up. This matches "catalog image and variant image is not working"
exactly: it isn't that images fail to serve, it's that updates to them look
like they silently do nothing.

**Fix**: `assetVersion` now checks `paths.Data(rel)` first, falling back to
the old cwd-relative `web/<rel>` path for built-in assets (app.css, vendor
JS, icons) that are never written into the stable dir.

**Verified live**: uploaded a photo to `itm001`, captured the returned
`?v=` value, re-uploaded a different photo, confirmed the returned `?v=`
value changed and `GET` on the new URL returned the new bytes (200,
`image/png`).

**Test**: `internal/httpx/asset_version_test.go` —
`TestAssetVersionReflectsStableDirUpdates` writes a file under a fake stable
dir, captures the version, rewrites the file with a later mtime, and asserts
the version changes. Confirmed this test fails against the pre-fix code
(reverted the fix locally, test failed with "both returned the same
version"; restored the fix, test passed).
`TestAssetVersionFallsBackToReleaseTreeForBuiltinAssets` guards the fallback
path for assets that are never in the stable dir.

## Bug 2: inventory search filter resets after a save

**Root cause**: the previous session's inventory auto-refresh fix (PR #81)
made `#stock-levels-card` re-fetch and swap in a fresh stock table
(`hx-trigger="stock-updated from:body"`) on every successful receipt/
override/return. The client-side search filter (`web/ui/pages/inventory.html`)
only ever bound its `input` listener once, at page load, and never re-ran
after that swap — so a freshly-fetched, unfiltered table replaced the
filtered one every time stock changed, making an active search (e.g.
"latte") appear to reset to "show everything."

**Fix**: extracted the filter logic into `applyStockFilter()` and added a
second listener — `htmx:afterSwap` on `#stock-levels-card` — so the filter
re-applies immediately after the table is replaced, using whatever the
search box currently contains.

**Verified live**: confirmed via `curl` that the rendered page includes the
`htmx:afterSwap` listener wiring, that `/ui/inventory/stock-table` still
returns 200, and that a stock receipt still returns
`HX-Trigger: stock-updated`. The actual re-filter behavior is JS executed in
a browser and can't be exercised by a Go test or curl — this is a known,
explicitly documented gap (see test comment in `ui_smoke_test.go`).

**Test**: extended the existing
`TestInventoryReceiptTriggersStockTableRefresh` to also assert the
`data-name`/`data-sku` attributes the client-side filter reads are present
in the partial's output, with a comment flagging that the re-filter-on-swap
behavior itself needs manual/browser verification.

## Additional test coverage added (per "test all scenarios, positive and
negative" request)

`internal/pages/catalog/image_upload_test.go` — new file, 9 tests covering
`POST /api/catalog/item/image` and `POST /api/catalog/variant/image`:

- Positive: upload persists to the stable data dir; a second upload
  overwrites in place (bytes actually change); variant photo is stored
  separately from and does not clobber the item's own photo.
- Negative: missing `item_id`/`variant_id`; path-traversal characters in
  `item_id`/`variant_id` (`../../evil`) rejected before touching the
  filesystem; unknown item/variant id (404); non-image file content
  rejected; missing file part rejected.

All new/modified tests pass (`go test ./...`), and both CI guards pass
(`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh`).

## Files changed

- `internal/httpx/httpx.go` — `assetVersion` checks the stable data dir first.
- `web/ui/pages/inventory.html` — filter re-applies after the HTMX table swap.
- `internal/httpx/asset_version_test.go` — new.
- `internal/pages/catalog/image_upload_test.go` — new.
- `internal/pages/ui_smoke_test.go` — extended existing test with a
  data-attribute assertion.
