# 2026-07-29 — Two catalog-item visibility bugs, found live

## Context
While setting up sample café items (Latte, Ham Sandwich, etc.) to test the
new dine-in/takeaway tax-switching feature, real bugs surfaced from actual
use of the catalog admin UI — not from code review.

## Bug 1: a new item never showed up on the Inventory page
`CatalogRepo.CreateItem` (and `CreateVariant`) only ever inserted into
`items`/`item_variants` — never `inventory`. `ListStockLevels` (the
Inventory page's query) starts from `FROM inventory inv JOIN items i ON
i.id = inv.item_id` — an INNER JOIN, so an item with zero inventory rows is
invisible there, even though it shows up fine in the catalog list (a
different query, no such join). `RecordStockMovement`'s own `UPDATE
inventory SET quantity = quantity + ?` can't create a missing row either —
it's a plain UPDATE, a no-op against zero matching rows. Nothing in the
codebase created that first row for a brand-new item.

**Fix**: both `CreateItem` and `CreateVariant` now call a new
`ensureInventoryRow` helper — finds or creates the default "Main" location
(same logic as `POSRepo.EnsureStockLocation`, kept local to avoid a
cross-repo dependency for five lines of SQL) and inserts a zero-quantity
`inventory` row, `INSERT OR IGNORE` against the existing unique index so a
re-run is a no-op.

**Judgment call, and the one that actually broke tests on the first pass**:
this must be genuinely best-effort, not just labeled that way. My first
version returned the inventory-row error from `CreateItem`/`CreateVariant`,
which immediately broke `TestCatalogCreateAndDeactivate` and
`TestCreateVariantAndBarcode` — both use a minimal hand-rolled test schema
with no `stock_locations` table, and turning that into a 400 on every item
creation is exactly wrong: an item is fully usable for sale without a
stock row (stock tracking is opt-in), so a failure here must only be
logged (`logging.L().Warnf`), never surfaced as item-creation failure.
Added `TestCreateItem_SucceedsWithoutStockLocationsTable` specifically to
lock this in — regression-tests the mistake, not just the feature.

## Bug 2: a catalog-uploaded item image never showed on the sale-screen tile
Two parallel, never-synced image sources: `item_images` (role='thumbnail')
— what the catalog list and the barcode/scan resolvers
(`internal/data/pos_repo.go`'s `resolve*` functions) already read — and
`shortcut_buttons.image_path`, a separate column only the sale-screen
product-tile grid (`ShortcutsRepo.LoadButtons`) reads. Uploading a photo
via the catalog form writes to `item_images`; nothing ever populates
`shortcut_buttons.image_path` from that upload. Confirmed live: an item's
photo appeared in the catalog list, never on its own tile.

**Fix**: `LoadButtons`' query now does `COALESCE(sb.image_path, (SELECT
path FROM item_images img WHERE img.item_id = sb.item_id AND img.role =
'thumbnail' LIMIT 1))` — falls back to the catalog image when the button
has no image of its own, same subquery pattern already used elsewhere. An
explicit button-specific image (set via the Designer) still wins when both
exist — the more specific choice should override the general fallback, not
the other way round.

## Not a bug: per-variant images
Reported alongside the above ("different variant can have different
images but there is no place to add it") — checked, and the UI already
exists: `catalog_variants.html`'s variant grid has a small 📷 icon
(`.vg-img-upload`) next to each variant's thumbnail, wired to the existing
`POST /api/catalog/variant/image` endpoint. Easy to miss (small, `opacity:
.6` until hover) but functional — no code change made here.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...`, both CI guard scripts,
`gofmt -l` all pass. New tests: `TestCreateItem_CreatesInventoryRow`,
`TestCreateVariant_CreatesInventoryRow`,
`TestCreateItem_SucceedsWithoutStockLocationsTable`,
`TestLoadButtons_FallsBackToCatalogImage`.
