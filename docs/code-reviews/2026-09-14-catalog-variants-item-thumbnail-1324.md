# Code review: variants panel resolves the item thumbnail through item_images (ut-docs#1324)

**Date:** 2026-09-14
**Card:** ut-docs#1324 — "item thumbnail resolves from disk (`imgExists`), not
`item_images`"
**Branch:** `fix/1324-catalog-variants-item-thumbnail`
**Diff:** `internal/pages/catalog/handlers.go` (`renderVariantsPanelWith`),
`web/ui/partials/catalog_variants.html`,
`internal/pages/catalog/variants_panel_thumbnail_test.go` (new)
**Reviewer:** independent (Opus, different model from the Sonnet that wrote
the code, own isolated git worktree, did not write the original diff)

## What shipped

ut-docs#1324 originally covered several surfaces. The BA step found all but
one had already been fixed by unrelated, already-merged cards — the admin
Catalog list (#1842), the self-order kiosk (#1870) and the AI-identify API
(#1875) all resolve an item's thumbnail through `item_images`
(`role='thumbnail'`) now. The one remaining gap was
`web/ui/partials/catalog_variants.html`, the per-item variants/barcodes admin
panel, which still guessed `/public/assets/items/<id>/thumb.png` and tested it
with `imgExists`. An item whose thumbnail is a built-in category icon (the
image picker, #1844; the CSV-import placeholder, #1189) has no file at that
guessed path, so this one panel showed a blank placeholder while every other
surface showed the icon correctly.

- `renderVariantsPanelWith` now calls the existing
  `CatalogRepo.ItemThumbnailFor(ctx, itemID)` (added by #1842, already used by
  `row_oob.go`) and puts the result in the panel's template data as
  `ItemImageURL`.
- The panel header renders
  `{{ if .ItemImageURL }}<img class="thumb" src="{{ imgv .ItemImageURL }}" …>{{ else }}<div class="thumb" …></div>{{ end }}`
  — byte-for-byte the convention `catalog_row.html` (#1842), `basket.html`,
  `buttons.html` and `suggestions.html` already use. No new rendering
  convention invented.
- The per-variant-row **fallback** (what shows when that specific variant has
  no photo of its own) uses `$.ItemImageURL` instead of a second disk-path
  guess, including inside the variant `<img>`'s `onerror` JS chain.
- The variant's **own** photo
  (`/public/assets/items/<item>/variants/<variant>/thumb.png`) is deliberately
  left disk-only and still takes priority — `item_images` has no `variant_id`
  column, so a variant has no row to resolve from. After this diff,
  `imgExists` has exactly **one** remaining use in the whole `web/ui/` tree
  (`catalog_variants.html:154`), and it is precisely that intended exception.
- 4 new tests in `internal/pages/catalog/variants_panel_thumbnail_test.go`.

## What the independent review found

**Verdict: safe to merge.** One comment-accuracy finding fixed during review
(F1), three accepted-as-correct with reasoning recorded (F2–F4), one real but
out-of-scope doc finding deferred (F5). No blocker-class findings; no
correctness, security or UI-behaviour defect found in the shipped code.

### F1 — fixed during review: a comment this diff added states a false rationale

Both the new template comment and the new test file's header comment justified
leaving variant photos disk-only with "*per-till, not synced*", citing
`docs/code-reviews/2026-07-17-variant-images.md`. That caveat was true when it
was written and is **stale now**. `internal/pages/sync_assets.go` (the
ADR-0011 D2b asset-sync follow-up, built later) defines
`assetsRoot() = paths.Data("public","assets","items")` and `listItemAssets()`
does a **recursive** `filepath.Walk` of it — which includes
`items/<item>/variants/<variant>/thumb.png`. `safeAssetPath` accepts that
relative path (no `..`, not absolute), and `syncItemAssets`'s replica pull
loop downloads every manifest entry. So variant photo **files do reach
replicas** today.

This matters beyond pedantry: a future reader deciding whether variant images
can move into `item_images` would be reasoning from a false premise about why
they are where they are. The real reason is structural and unchanged —
`item_images` has no `variant_id` column — and that half of the comment was
already correct.

**Fix:** both comments now give the structural reason only and explicitly flag
the 2026-07-17 sync caveat as superseded, naming `sync_assets.go` so the next
reader can check. Comment-only; no rendered output or behaviour changed
(re-verified: all 4 new tests plus the full package still green, and the
rendered HTML dump below is unchanged). The `handlers.go` comment needed no
change — it says only "no `variant_id` column on `item_images`", which is
accurate.

### F2 — accepted: the TDD claim is "3 of 4", not "4 of 4"

Re-verified personally rather than taken on trust (method below).
`TestVariantsPanel_ItemHeaderThumb_NoImageShowsPlaceholder` **passes against
the pre-fix code too** — an item with no `item_images` row also had no file at
the guessed path, so both the old and new code reach the placeholder `<div>`.
It is a legitimate no-regression guard on the `else` branch, not a pin on the
bug, and the same shape #1870's review recorded for its own
`TestSelfOrderShop_GridStillShowsUploadedPhoto`. Kept as-is; recorded here so
the TDD claim is not overstated.

### F3 — accepted: the header `<img>` is no longer `imgExists`-guarded

`imgExists` exists (#319) to avoid rendering an `<img src>` the browser will
always 404 on. The header now renders the `<img>` whenever an `item_images`
row exists, without checking the file. If a row is ever stale — a path whose
file is missing — the panel issues one doomed request, and a panel with N
photoless variant rows issues N (identical URL, so the browser dedupes after
the first). Accepted because: (a) it is exactly what `catalog_row.html` does
after #1842, and diverging would re-invent the convention this card exists to
converge on; (b) both the header and the row `<img>`s carry `onerror`
handlers, so nothing visibly breaks; (c) #319's concern was a per-row doomed
request across a whole list, not one item's detail panel. Net, the diff
*reduces* doomed requests: the old code's variant `onerror` set
`this.src='<guessed item path>'` **unconditionally**, with no `imgExists`
guard at all, so a photoless item guaranteed a second failed request per
variant row; the new code emits `this.style.visibility='hidden'` in that case
instead.

### F4 — accepted: `itemImg, _ := repo.ItemThumbnailFor(...)` swallows the error

Correct here, and consistent both ways. Inside `renderVariantsPanelWith` every
sibling repo call is already best-effort (`VariantsForItem`,
`BarcodesForItem`, `ItemCostPrice`, `ItemLeadTimeDays`, `ItemReorderLevel`,
`ListAllGroupsForItem`, `ListOptionSets`, `ItemOptionSets` — all `x, _ :=`),
and the closure has no error return to surface one through. `row_oob.go`
propagates the same call's error only because *its* function returns `error`.
`""` on error degrades to "no image", identical to a genuinely absent row, on
a panel where the thumbnail is decoration rather than data — the same
best-effort reasoning #1870's review recorded for `ItemThumbnails`.

### F5 — deferred (out of scope, #1870's debt): stale sentence in the help manual

`web/help/en/catalog.md:21` still tells operators "*the self-order screen
shows a blank tile rather than the built-in image (it does not read this
choice yet)*". That is no longer true: `internal/pages/self_order_shop.go:96`
sets `ImageURL: thumbnails[it.ID]` from `item_images` after #1870. Present in
`en` only (de/ar/fa/tr do not carry the sentence). Not touched here — it is
#1870's documentation debt, not this card's, and fixing it would put an
unrelated behaviour claim in this diff. Recommend a follow-up card.

**No help update is needed for #1324 itself.** No help topic documents the
variants panel's thumbnail behaviour, and nothing an operator sees or does
changed: the same icon appears in the same place, correctly now instead of
sometimes-blank. (`catalog.md:21` and `:34` enumerate where an item's image
shows — "sale screen, basket, search results and the Catalog list" — without
claiming the Variants tab does *not*, so neither line becomes wrong.)

## Verified beyond the automated tests

**Own revert-verify of the TDD claim.** Restored both source files to the
pre-fix parent (`git show HEAD^:internal/pages/catalog/handlers.go`,
`git show HEAD^:web/ui/partials/catalog_variants.html`) and reran
`go test ./internal/pages/catalog/... -run TestVariantsPanel -v`:

```
--- PASS: TestVariantsPanel_UnknownItemRendersEmptyState
--- FAIL: TestVariantsPanel_ItemHeaderThumb_ResolvesFromItemImages
--- PASS: TestVariantsPanel_ItemHeaderThumb_NoImageShowsPlaceholder   <- F2
--- FAIL: TestVariantsPanel_VariantRow_FallsBackToItemImage
--- FAIL: TestVariantsPanel_VariantRow_OwnPhotoTakesPriorityOverItemFallback
```

Each failure carried exactly the expected pre-fix symptom (the seeded
`item_images` icon absent from the rendered panel). Restored with
`git checkout HEAD -- …` and confirmed 5/5 green again.

**Rendered the actual HTML, rather than reading the template.** The `onerror`
JS string is the riskiest part of the diff — it now interpolates a
DB-sourced value into a JS string literal inside an HTML attribute. Wrote a
throwaway test (since removed) that seeded a deliberately hostile
`item_images.path` — `/public/assets/icons/it's "quoted" & <odd>.svg` — and
dumped the response:

```
onerror="if(!this.dataset.fb){this.dataset.fb=1;this.src='\/public\/assets\/icons\/it\u0027s \u0022quoted\u0022 \u0026 \u003codd\u003e.svg?v=1789376509';}else{this.style.visibility='hidden';}"
```

`html/template`'s contextual escaper correctly applied the JS-string escaper
inside the attribute — the apostrophe came out as `\u0027`, the double
quote as `\u0022`, and `&`/`<`/`>` as `\u0026`/`\u003c`/`\u003e`, so there is no
attribute breakout and no JS breakout. The `src` attribute in the same row got
URL escaping (`it%27s%20%22quoted%22…`) as it should. The no-item-image branch
renders valid JS too:
`if(!this.dataset.fb){this.dataset.fb=1;this.style.visibility='hidden';}else{this.style.visibility='hidden';}`
— both `{{ if }}` arms leave the escaper in the same JS-statement context, so
the conditional inside the attribute is well-formed, not a context split.

**Template `$` scoping confirmed by execution, not by reading.**
`$.ItemImageURL` inside `{{ range .Variants }}` resolves to the root data
passed to `Execute`, not the range's dot — proven by
`TestVariantsPanel_VariantRow_FallsBackToItemImage` and the HTML dump above,
both of which show the item-level icon path rendered from inside a variant
row.

**Markup-preserving check.** Confirmed the change is data-source-only: the
element set (`<img class="thumb">` / `<div class="thumb">` in the header;
`<img>` / `<img>` / `<div class="thumb-ph">` in the row), every class name,
the `alt`/`aria-hidden` attributes and the `onerror` strategies
(`display='none'` in the header, `visibility='hidden'` in rows) are all
unchanged. Only the Go-template expression powering the `if` and the `src`
moved. No CSS was touched; no new class or element was introduced.

**Both bug classes this pipeline repeatedly finds: checked, neither present.**
The diff adds no file-write handler, so no missing `os.MkdirAll` is possible;
the one file write in the new tests does call `os.MkdirAll` first. No raw
cwd-relative disk path was introduced — the tests build their paths through
`paths.Data(...)` and sandbox with the package's own established
`paths.Init(t.TempDir())` + `t.Cleanup(func(){ paths.Init("") })` pattern
(matching `handlers_errors_test.go`, `icon_picker_test.go`,
`image_upload_test.go`, `lookup_endpoints_test.go`). `paths.Init("")` restores
the unset sentinel — i.e. the package default, not a leaked temp dir — and no
test in this package uses `t.Parallel()`, so the global is not raced.

**Test data.** No real shop or client name used — `Widget`, `Plain`, `Small`,
`Large`, `itm1`–`itm4`, `var3`/`var4`, `coffee.svg`/`pastry.svg`/`drink.svg`.

**Completeness.** Grepped the whole `web/ui/` tree and `internal/pages/` for
remaining `/public/assets/items/<id>/thumb.png` guesses and `imgExists` uses.
The only `imgExists` left anywhere in `web/ui/` is the variant's own photo
check, which is the deliberate exception. The remaining literal item-thumb
paths in Go are `SetItemThumbnail` **writers** (`handlers.go:905`, `:1355`)
establishing the upload path convention, which is correct — they write the row
that readers like this one now resolve through.

## Commands run (all from an isolated worktree at `ff51e42d`)

| Command | Result |
| --- | --- |
| `go build ./...` | ok |
| `go vet ./...` | ok (whole module, no findings) |
| `gofmt -l internal/pages/catalog/` | no output (clean) |
| `golangci-lint run ./internal/pages/catalog/...` | `0 issues.` |
| `go test ./internal/pages/catalog/...` | `ok … 1.685s` |
| full suite (`go list ./...` minus `/internal/plugins`, `/internal/plugins/{oauth,marketplace}`, `/internal/pages`; 73 pkgs) | exit 0 — 56 `ok`, 17 no-test-files, 0 failures |
| `bash scripts/ci/guard-data-access.sh` | `✓ no inline SQL outside internal/data / internal/db` |
| `bash scripts/ci/guard-i18n.sh` | `✓ 1676 template keys resolve; all locales match en.json` (+ all 7 sub-checks clean) |

Every gate above was re-run **after** the F1 comment fix, with identical
results.

## Explicitly deferred

- **F5** — `web/help/en/catalog.md:21`'s stale self-order claim (#1870's
  debt). Needs its own card; not folded into this diff.
- Variant photos remain outside `item_images`, and the sale screen / kiosk
  still surface the item image for a variant rather than the variant's own —
  the original 2026-07-17 spec follow-up, untouched and still open.
- The `docs/code-reviews/2026-07-17-variant-images.md` record itself still
  carries the superseded "per-till" caveat. Left as a historical record (a
  review record describes what was true when written); the live code comments
  that a developer will actually read were corrected instead, under F1.
