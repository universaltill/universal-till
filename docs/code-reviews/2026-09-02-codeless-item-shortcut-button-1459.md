# Code review: catalog items with no barcode/SKU can be added as sale-screen buttons (ut-docs#1459)

## What shipped

`ut-docs#1459`: a catalog item with **neither a barcode nor a SKU** could
be found by the Designer's search but could never actually be added as a
sale-screen shortcut button — `ButtonStore.Add` rejected an empty `code`
outright with a 400, and `AddVals`'s barcode→SKU fallback (ut-docs#1220)
has nothing left to fall back to once both are blank. Reported live from a
real clean-catalog-import restore: 144 of 229 real café items had neither
identifier and were completely unsellable from the till.

**The fix.** `internal/ui/buttons.go`, `ButtonStore.Add`: when `Code` is
still empty after trimming (and `ItemID` is non-empty), synthesize a
stable code — `"item:" + ItemID` (`synthesizedButtonCodePrefix` constant)
— before persisting, instead of rejecting the add. `shortcut_buttons.barcode`
is that table's `TEXT PRIMARY KEY`, so this is what actually gets stored as
the button's code going forward; no schema or repository-layer change, no
new caller-visible API shape. Item IDs are always server-generated UUIDs
(`uuid.NewString()`, `catalog_repo.go`), so the synthesized code is unique,
stable across re-adds (upserts in place via the existing `ON
CONFLICT(barcode)`), and cannot collide with a real scanned barcode or a
human-entered SKU.

## Independent review (Opus, isolated worktree — different model from the
one that wrote the fix)

Verdict up front: **the core fix was correct and independently
re-verified (TDD red/green, at both the Go and e2e layer) — not safe to
merge as first submitted.** Two real findings, both fixed before merge:

1. **Real client name reintroduced (should-fix).** Two new files' comments
   said "the reference Haaft CSV." `docs/code-reviews/2026-08-21-hold-
   sale-real-client-name-521.md` established Haaft is a real German café's
   real name and purged it from the codebase; this diff put it back
   (comments only — no behavior change, but re-breaks an already-remediated
   rule). **Fixed**: reworded to "the reference café catalog CSV" in
   `internal/ui/buttons_test.go` and
   `e2e/tests/codeless-item-shortcut-1459.spec.ts`.
2. **Synthesized code leaked as a raw UUID onto receipts/journal
   (should-fix).** `data.POSRepo.toShortcutLine` sets `ShortcutLine.SKU =
   code` for a shortcut-button match — pre-existing behavior, but `code`
   is now sometimes the synthesized `item:<uuid>` string rather than a
   real barcode/SKU. That value flows through
   `PriceResolverAdapter.resolve` into `pos.BasketLine.SKU`, then to
   `sale_lines.sku_snapshot`, and prints on the receipt / shows in the
   journal detail's SKU column when the shop has "Show SKU" on — the same
   raw-internal-id-leak class ut-docs#1176 already fixed once for the
   catalog's own SKU column. **Fixed**: `PriceResolverAdapter.resolve`
   now blanks `SKU` when it carries the `synthesizedButtonCodePrefix`
   prefix, scoped narrowly to that one call site (not a change to
   `resolveShortcut`'s SQL or `toShortcutLine`'s generic behavior, which
   would have risked changing what real barcode/SKU-keyed shortcut buttons
   show — the AC's own "existing SKU/barcode buttons keep their current
   behaviour" requirement).

Three nitpicks, also fixed:
- A pre-existing test's "raw error must not leak" guard string
  (`buttons_http_test.go`) had gone vacuously true after this diff's own
  earlier retarget of that test (from "missing code" to "missing label" —
  the only viable trigger left once empty code stopped being an error).
  Updated the asserted literal and message to match.
- `AddVals`'s doc comment still claimed a codeless item's `code=""` gets
  rejected as a 400 by `Add` — no longer true. Reworded to describe the
  current split of responsibility (`Add` is the single choke point that
  copes with "no code at all"; `AddVals` is unchanged, a per-template
  view-model helper).
- The card's own AC says "rings up at the right price **and tax**" but
  neither the original Go test nor the e2e asserted tax. Added a
  `TaxRateBP` assertion (and a `SKU` blank-check, covering finding 2
  above) to `TestButtonStoreAdd_SynthesizesCodeWhenNeitherBarcodeNorSKU`.

One item explicitly accepted as out of scope, not introduced by this
change: if a codeless item later gains a real barcode/SKU and is re-added
from the Designer, the new add posts the real code and the `ON
CONFLICT(barcode)` upsert won't match the old `item:`-prefixed row,
producing a second tile for one item. Same shape as the pre-existing
SKU→barcode transition gap (ut-docs#1220); not new here, not fixed here.

## Verification (re-run after the review's fixes, not just before)

| Check | Result |
|---|---|
| `gofmt -l .` | clean |
| `go build ./...` / `go vet ./...` | clean |
| `go test ./...` (every package) | all pass |
| `guard-data-access.sh` / `guard-i18n.sh` | pass / pass (1342 keys) |
| Playwright: `codeless-item-shortcut-1459` + `designer-search` + `designer-reorder-1221` + `designer-reorder-buttons-overflow-1354` + `category-switch-stale-tile-add-1433` + `catalog-barcode-backfill-1356` + `sale-screen-213`/`sale.spec` + others (45-51 specs across two runs) | all pass |

**TDD independently re-verified twice** (once by the reviewer subagent in
an isolated worktree, once again here after the review's own fixes):
1. Reverting only `ButtonStore.Add`'s synthesis hunk: both new Go unit
   tests fail with `label, code, and itemId are required` / the old
   rejection; the new e2e spec fails at the exact assertion where the tile
   should have appeared (`toHaveCount` — 0 elements, not 1). Restoring the
   fix returns all to green.
2. Reverting only the SKU-blanking hunk in `PriceResolverAdapter.resolve`:
   the new `SKU` assertion fails with `SKU = "item:itm1", want blank`.
   Restoring the fix returns it to green.

## What was verified beyond automated tests

- Grepped for every production path that creates an `items.id` (all
  `uuid.NewString()`, no user-suppliable item id) to confirm the
  synthesized-code collision argument actually holds.
- Traced the full resolve chain (`ResolveScanLine` → `resolveShortcut` →
  `PriceResolverAdapter.resolve`) to confirm a `item:<uuid>`-shaped code
  never gets misidentified as a barcode symbology (no symbology match ⇒
  clean fall-through to the shortcut tier) and that a comma-split reorder
  path (`buttons_api.go`'s `/api/buttons/reorder`) can't be corrupted by
  it (UUIDs contain no comma).
- No real client/shop name in the final diff (the one instance found was
  fixed, see above). No secret-shaped literals anywhere in the diff.
- Not a UI/visual change — no template, CSS, or new user-facing string
  touched (pure Go domain-layer fix), so no screenshot/theme/RTL check
  was owed here; the e2e spec is the real-browser proof of the actual
  operator flow (CSV import → Designer search-and-tap → real sale-screen
  tap → basket).
- No disk I/O in the diff (confirmed by grep for
  `os.Create|WriteFile|MkdirAll|Open|paths.|filepath.`), so the two
  recurring bug classes this pipeline watches for (missing `MkdirAll`,
  cwd-relative path instead of `paths.Data`) don't apply.

## Safe-to-merge verdict

Yes, after the two should-fix findings and three nitpicks above were
applied and the full gate + affected e2e suites re-run green.

## Explicitly deferred / not in scope

- The pre-existing SKU→barcode-transition duplicate-tile gap (see above) —
  same shape as ut-docs#1220's own known limitation, not introduced here.
- No manual/`web/help/` topic update: this change has no visible surface
  (no new screen, no changed operator-facing flow/copy) — the Designer's
  add-as-button flow behaves identically from the operator's point of
  view, it simply now succeeds for an item that used to 400 silently.
