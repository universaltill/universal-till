# 2026-09-14 — Sale screen: same-named subcategories under different top-level categories are now distinguishable (ut-docs#2198)

## What shipped

ut-docs#2181 made the sale-screen search span every category at once and
gave every panel a category label so a cross-category result is
attributable — but a nested subcategory's label was only ever its own
bare `.Name`. Two different top-level categories each having a
same-named subcategory (e.g. `Food > Specials` and `Household >
Specials`) rendered two identical "Specials" headers with nothing to
tell them apart, once ut-docs#2212 made "All" the default tab (so both
top-level categories' content, and therefore both "Specials" sections,
are already visible simultaneously by default — not only during an
active search).

- **`internal/ui/buttons.go`**: `CategoryGroup` gained an `AncestorName`
  field — empty for a root, set to that root's own `Name` for every
  descendant at every depth (a grandchild still gets the top-level name,
  not its immediate parent's — "at least the top-level category name" is
  what the card asked for, not a full breadcrumb). Computed once in
  `BuildCategoryGroups` via a new `setAncestorNames` helper, after
  pruning and before the synthetic uncategorized bucket is appended (so
  that bucket, which never has children anyway, is untouched).
- **`web/ui/partials/buttons.html`**: the `category-group` template's
  `<h3>` gained a `{{ if .AncestorName }}<span class="category-header-
  ancestor" x-show="q || tab === '__all__'">{{ .AncestorName }} ›
  </span>{{ end }}` prefix. The `x-show` condition is copied verbatim
  from the sibling top-level header in `category-group-body-tabbed` —
  reusing an expression this codebase's own e2e suite already exercises,
  rather than inventing a second one that could drift from it. Guarded
  by `{{ if .AncestorName }}` so a root (including the single-real-
  category, no-tab-bar branch, which calls `category-group` directly on
  roots) never grows a spurious prefix. No new CSS — inline text only,
  and the `›` separator is a bare glyph needing no `{{ T }}` key.
- **`internal/ui/buttons_category_groups_test.go`**: new
  `TestBuildCategoryGroups_AncestorNameLabelsDescendantsNotRoots` pins
  the Go-side computation, including a 3-level (grandchild) case.
- **`internal/ui/buttons_search_visibility_test.go`**: new
  `TestButtonsHTTPList_SameNamedSubcategoriesCarryDistinctAncestorLabels`
  pins the rendered markup for the exact two-same-named-subcategories
  scenario, and that a root's own header never gets prefixed.
- **`web/help/img/{en,ar,fa,tr}/sell.png` + `manifest.json`**: regenerated
  via `make docs-shots` — the demo catalog's `Food > Dairy` subcategory
  is visible by default (All tab), so its header now reads "Food ›
  Dairy" instead of "Dairy" in the manual's own screenshot. Confirmed via
  `git status` that only the `sell` topic's 4 locale screenshots +
  manifest changed — no other topic's surface was touched.

## Independent review

Sonnet, fresh context, isolated worktree.

**What it did, beyond reading the diff:** ran `go build`/`vet`/`test`
(both the touched package and the full repo test suite separately, by
me, both green), `golangci-lint`, `guard-i18n.sh`, `guard-docs-shots.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`. Did TDD re-verification:
commented out the `setAncestorNames` call, confirmed both new tests fail
with real assertion messages (not compile errors), restored, confirmed
green again, confirmed no residual diff.

**Verdict: PASS, safe to merge, no blockers.**

**Finding (medium/nit, not fixed, filed as a follow-up):** when a till
has exactly one real top-level category (`$hasTabs` false), `tab` never
becomes `'__all__'` (no such tab exists in that branch), but `q` (an
active search) still can be. The ancestor prefix's gate is `q || tab ===
'__all__'`, copied from the multi-root sibling header under the
rationale "shown while more than one root's content can be visible at
once" — an invariant that never actually holds in the single-root case.
A nested subcategory there will still show a harmless-but-unnecessary
"Food › Specials" prefix while searching, even though nothing else on
the till could ever share that subcategory's name. The label itself is
still accurate (not wrong information), and doesn't affect the common
case (≥2 top-level categories) at all — filed as
`universaltill/ut-docs#2228` rather than held for a second review round,
per this pipeline's "second round is earned by a blocker-class finding"
rule (money/tax/data-loss/security), which this isn't.

**Other checks, verified clean:** the three `category-group` call sites
(no-tabs root branch, top-level's-own-children branch, its own recursive
Children branch) confirm a root can never receive a spurious
`AncestorName`; no new file I/O (neither recurring `os.MkdirAll`/
`paths.Data` bug class applies — this is a pure in-memory/template
change); `CategoryGroup` is template-only, never JSON-marshaled, so
`AncestorName` needing no `json:` tag is consistent with the rest of the
struct; `web/help/en/sell.md`'s existing prose ("each match shows a
category label so you know where it came from") stays accurate and
needed no update, since it never specified the exact label format;
RTL spot-check of `ar/sell.png` shows "Food › Dairy" correctly rendering
as an embedded LTR run inside the RTL chrome (same as the pre-existing,
already-shipped top-level header), and `›` is bidi-mirroring per Unicode
so it would self-correct if ever surrounded by genuinely RTL-scripted
category names.

## Verified beyond automated tests

- Real browser run (not just the Go template-rendering tests): started
  the actual till binary (`e2e/run-till.sh`, demo catalog seeded) and
  drove it with Playwright/Chromium at both the 1024×600 kiosk floor and
  360px phone width. Confirmed live: the default All-tab view shows "Food
  › Dairy"; a cross-category search ("a") shows "Food › Dairy" alongside
  the unprefixed root header "Drinks"; explicitly selecting the single
  "Food" tab (not All) shows plain "Dairy" — unchanged, matching the
  at-rest behavior the acceptance criteria require. Screenshots looked at
  directly, not just asserted on.
- Ran the existing targeted e2e regression suite this markup touches
  (`sale-screen-category-tabs-search-418.spec.ts`,
  `sale-screen-search-strip-2173.spec.ts`,
  `product-tile-category-color-1325.spec.ts`,
  `tab-bar-overflow-aria-424.spec.ts`) — 19/19 passed, no regressions.
- Full repo `go test ./... -count=1` and `golangci-lint run ./...` both
  green, run separately from the review subagent's own scoped run.
- `make docs-shots` regenerated for real (not skipped); `guard-docs-
  shots.sh` re-confirmed green afterward, and the resulting diff is
  scoped to exactly the `sell` topic's 4 locale screenshots.

## Deferred

- `universaltill/ut-docs#2228` — tighten the ancestor-prefix gate to also
  require more than one real top-level category, so a single-category
  till never shows a redundant "Food › Specials" prefix while searching.
  Real but low-severity; not urgent.

## Safe to merge

Yes.
