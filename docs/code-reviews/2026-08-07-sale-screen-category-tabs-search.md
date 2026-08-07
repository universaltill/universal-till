# Code review: till sale screen category tabs + search (ut-docs#418)

**Date:** 2026-08-07
**Author (Dev):** scrum-master pipeline, Sonnet (complexity:medium)
**Reviewer:** independent Opus subagent (fresh context, different model)
**Card:** universaltill/ut-docs#418, split from ut-docs#402 (self-order's
half is ut-docs#419, separate card)

## What shipped

The till sale screen's product panel gets category tabs and a search box.
Previously the panel was a flat/stacked list of category sections with no
way to switch between categories or find an item by name.

Design approach: no new backend endpoint, no new SQL, no ADR. Categories
were already modeled and grouped server-side (`ui.BuildCategoryGroups`,
unchanged); this change is template + CSS only. `web/ui/partials/buttons.html`
renders the existing category groups as an Alpine.js tab bar (the same
`x-data`/`x-show`/`:class` idiom the pre-existing Pay/Split tender panel
already uses) instead of stacked `<section>`s, and adds a search
`<input x-model="q">` with each tile getting `data-name` +
`x-show="!q || $el.dataset.name.toLowerCase().includes(q.toLowerCase())"`.
Both filters (active tab, search query) are pure client-side — no server
round trip — deliberately, to avoid the compose bug ut-docs#419 fixes on
the self-order kiosk side (there, search reloads via a separate endpoint
that silently drops the active category filter).

Three render paths, by category count:
1. Zero real categories (only the synthetic "uncategorized" bucket) → flat
   grid, no header, no tab bar — unchanged from before this feature.
2. Exactly one real category → single headed `category-group`, no tab bar
   (nothing to switch between).
3. ≥2 real categories → tab bar + one `products-tab-panel` per category.

## Independent review — findings

One BLOCKING issue found, fixed, and pinned with a new test that exercises
the actual branch it lives in. Several non-blocking notes; two cheap ones
fixed in the same round (a documentation/hardening comment, and a manual
wording gap), the rest deferred as new Backlog cards since they're real
but out of this card's scope.

### Fixed: category colors were dropped in the ≥2-category branch (blocking)

The reviewer rendered (not just read) two categories with distinct
explicit colors and confirmed `--cat-color` and the color-coded header
were **completely absent** from the ≥2-category tab-bar branch — the
pinned `TestButtonsHTTPList_RendersNestedColorCodedGroups` test only
seeds one root category, which takes the *other* branch (`category-group`,
unchanged since before this feature), so it kept passing while asserting
nothing about the branch real multi-category tills actually take. A
classic false-pass shape: green test, dead coverage on the path that
matters.

Fix: each tab button now carries `style="--cat-color: {{ .Color }}"`; the
shared `.tab-bar .tab`/`.tab.active` CSS rules now consume it (with a
`var(--cat-color, var(--accent))` fallback so the tender panel's plain
Pay/Split tabs, which never set `--cat-color`, render exactly as before).
Colors are visible both on the active tab's underline and as an
always-on `border-block-start` accent on every tab, so at-a-glance
color-coding across categories survives the switch from stacked sections
to tabs. New test `TestButtonsHTTPList_TabsCarryColorWithMultipleCategories`
seeds two categories with distinct explicit colors and asserts both
survive verbatim in the tab bar — verified it fails without the fix
(reverted the CSS/template fix locally, confirmed the new test catches
the regression, restored the fix, confirmed green again).

### Fixed (cheap, same round): two non-blocking notes

- **Category-ID-into-Alpine-JS-string interpolation** (`x-data`, `:class`,
  `@click`): `html/template` escapes this as a plain HTML attribute, not
  a JS string context, so a literal `'` in a category ID would break the
  Alpine expression. Traced every category-creation path
  (`CatalogRepo.EnsureCategory`/`EnsureCategoryUnder`, the fixed `cat_*`
  seed literals) — IDs are always server-generated UUIDs, never
  caller-supplied text, so this is safe by construction today. Added an
  explicit comment on the invariant (pointing at `SearchResult.AddVals()`'s
  marshal-server-side pattern as the fix if that invariant ever changes)
  so it's not an unstated assumption.
- **`.tab-panel` reuse inherited tender-specific sizing** (a `min-height:
  6rem` floor and `overflow-y: auto` tuned for ut-docs#161's tender-panel
  flex-collapse bug, not applicable here since `.products` itself already
  scrolls). Split into a dedicated `.products-tab-panel` class without the
  borrowed floor/scroll, decoupling future tender-panel sizing changes
  from the sale screen.
- Also added `x-cloak` to non-default tab panels (mirrors
  `index.html`'s own Pay/Split panel, which cloaks "split" but not the
  default-visible "pay" — same reasoning applied here) for consistency
  with the pattern this change is explicitly modeled on.
- Manual wording: the rewritten step 1 in `web/help/*/sell.md` described
  *finding* an item via tabs/search but dropped the "tap the tile to add
  it" step the original text had. Restored in all 4 locales.

### Deferred to new Backlog cards (real, but out of this card's scope)

- Search leaves stranded, empty subcategory headers when a query matches
  nothing in a subcategory that still has other stocked tiles elsewhere
  in the tree, and there's no "no matches" affordance when a query
  matches nothing in the active tab at all.
- The search input has no Enter/blur handling and isn't excluded from
  scanner focus — a wedge-scanner scan while focus is in search types the
  barcode into the filter instead of ringing up an item.
- No wrap/scroll handling if a shop configures many (~10+) top-level
  categories — tabs would squash unreadably.
- ARIA is incomplete (no `aria-selected`/`aria-controls`/arrow-key nav) —
  matches the pre-existing tender tab bar exactly, so not a regression,
  but worth a dedicated a11y pass across both tab bars together.

## Verified beyond automated tests

- Live-rendered the sale screen via a real till server (not just
  `httptest`) at all three category-count branches, confirmed the
  markup, then regenerated the manual's actual screenshots
  (`make docs-shots` equivalent) and visually inspected all 4 locales
  (en/ar/fa/tr) — category tab colors, RTL mirroring (Arabic/Farsi),
  and translated placeholder text all confirmed correct by eye, not just
  by guard script.
- New Playwright e2e spec
  (`e2e/tests/sale-screen-category-tabs-search-418.spec.ts`) drives a
  real browser against the actual seeded demo catalog: tab switching,
  search composing with the active tab (not overriding it), and an RTL
  locale pass where a tile is confirmed to be a real, clickable hit
  target — not just present in the DOM.
- Independently re-verified the TDD claim on the blocking fix: reverted
  the CSS/template change, re-ran
  `TestButtonsHTTPList_TabsCarryColorWithMultipleCategories`, confirmed
  it fails with the expected "colors absent" symptom, restored the fix,
  confirmed it passes again.
- Confirmed the one failing test in the full `go test ./...` run
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`) is
  pre-existing and unrelated: reproduces identically on a clean `main`
  checkout with none of this diff applied, root-caused to this
  environment running `go test` as uid 0 (already tracked as
  ut-docs#415).

## Gate — all green

`go build ./...`, `go vet ./...`, `go test ./internal/ui/... ./internal/pages/...`,
`bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`,
`bash scripts/ci/guard-help-topics.sh`, `bash scripts/ci/guard-docs-shots.sh`.

## Verdict

**Safe to merge.** One real regression found and fixed with coverage that
actually exercises the branch it lives in; everything else is either
accepted-by-construction (documented) or a legitimate follow-up captured
as a new Backlog card, not silently dropped.
