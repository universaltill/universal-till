# 2026-09-24 — Categories tab: item-only categories (ut-docs#2498)

## What shipped

The sell screen's Categories tab (and the classic per-category tab bar
underneath it — both consume the same `.Groups`) silently pruned any
category that had zero manually-configured quick buttons anywhere in its
subtree, even when it held real active catalog items. A shop that added a
category and items to it, but hadn't yet wired up quick buttons, could not
reach that category from the sell screen at all — not as a tab, not as a
tile, not through the "…" overflow sheet.

`internal/ui/buttons.go`'s `BuildCategoryGroups`/`pruneEmptyCategoryGroup`
now also keeps a category alive via an active-item count (reusing
`CatalogRepo.ListCategoriesForAdmin`'s existing per-category `ItemCount`,
hoisted out of the `EditMode`-only branch in `ButtonsHTTP.List` to run
unconditionally — still exactly one query either way, the `EditMode`
branch reuses the same rows). A new `CategoryGroup.HasButtons` field tells
the template whether a kept group has any button anywhere in its subtree;
when it doesn't, the panel/tile picker renders a translated empty-state
message (`products.category_no_buttons`, added to `en.json`/`ar.json`/
`fa.json`/`tr.json`) instead of a blank grid.

Out of scope, confirmed unchanged: categories with zero active items stay
hidden; deactivated categories stay hidden (ut-docs#1898); nested
subcategories still flatten into their top-level ancestor's panel rather
than getting their own top-level tab (ut-docs#2283) — this card doesn't
touch either boundary.

## Root cause verification

Read `internal/ui/buttons.go` and `web/ui/partials/buttons.html` directly
(not just the bug report) before designing a fix. Confirmed via
`internal/ui/buttons_categories_tab_test.go`'s existing tests that the
"hides zero-active-item category" and "flattens nested subcategories"
tests always paired an item with its own button, so the actual "items but
no button" gap this card fixes had no prior coverage — the pruning
predicate itself was buttons-only, not items-only, contrary to what the
old test names implied. Confirmed independently that this is unrelated to
#2468/#2307 (a CSS/JS overflow fix over the pre-existing, already-rendered
`.Groups` — no interaction with which categories are IN `.Groups`).

## Independent review (Opus, different model from Dev's Sonnet)

Re-verified the TDD claims for real: reverted `buttons.go`/`buttons.html`
to pre-fix state (keeping the new/changed tests), confirmed the new tests
fail — a compile error against the unit-test file (signature change) and
real assertion failures against the HTTP-level tests — then restored and
confirmed everything passes again. Full output captured in the review
agent's report; not just a paraphrased claim.

**Findings, all fixed before merge:**

1. **Four e2e specs would have gone stale** — `sale-screen-category-strip-
   overflow-2307.spec.ts`, `sale-screen-search-strip-2173.spec.ts` (×2
   assertions), `tab-bar-overflow-aria-424.spec.ts` — all hardcoded a tab
   count assuming only Food/Drinks show. Household/Produce (real items, no
   buttons in the demo seed) now correctly show as tabs too. Counts and
   comments updated to match (2→4 demo category tabs; `+3`→`+5` in the
   generic-category-count specs).
2. **Default landing tab could be an empty one** (real bug, not just a
   test count): with the All tab off, the sell screen used to default to
   `Groups[0]`. A category kept only via item count sorts first in a shop
   whose first-added category has no quick buttons yet, so the operator
   would land on the empty-state message instead of real quick buttons on
   first paint. Fixed: `buttons.html`'s `$defaultTab` computation now
   prefers the first group with `HasButtons`, falling back to `Groups[0]`
   only when nothing has a button yet (unaffected: a shop with zero quick
   buttons configured anywhere, same as before).
3. **Headerless duplicate empty-state message**: a top-level category
   with children but no buttons/items of its own rendered a bare `<p>`
   message (no `<h3>` — `category-group-body-tabbed`'s header is
   `if .Buttons`-gated) immediately followed by each surviving child's own
   headed section repeating the same message. Fixed: the parent's own
   bare message only renders when it has no children at all; when it does,
   control falls straight to the children, each of which already carries
   its own real header (`category-group`'s header is unconditional).
4. **Help doc contradicted the new behaviour**: `web/help/en/till-
   designer.md`'s "Manage categories" step claimed the strip never shows a
   category with no quick buttons. Reworded to describe the actual rule
   (hidden: deactivated, or neither buttons nor items; shown-with-a-note:
   items but no buttons yet). `guard-help-drift.sh` stayed green (no new
   structural drift — wording-only change, same list-item shape).
5. **Stale test-name reference** in a comment (`buttons_all_tab_test.go`)
   pointing at a test name that didn't end up matching the one actually
   added — corrected.

**Confirmed correct, not changed:** locale files (en/ar/fa/tr) all carry
the new key with matching key counts (2657 keys each); nil-map read of
`itemCounts[g.ID]` is a safe zero-value, never a panic; `HasButtons`
propagation traced by hand through a 3-level mixed tree, no false
positive/negative; the flipped `TestButtonsHTTPList_AllTabShowsItemWithNo
QuickButton` assertion is a legitimate correction (it pinned the exact bug
this card fixes as "unchanged pre-existing behavior" from ut-docs#2294's
own scope note — not a requirement that such categories stay hidden
forever).

## Verified beyond automated tests

Real driven run (not just `httptest` assertions) against a built binary,
fresh demo-seeded data dir, real Chromium via Playwright:

- 1024×600 (kiosk floor) and 360×800 (phone floor): all 4 demo category
  tabs present (2 visible + 2 behind the pre-existing "…" overflow at
  1024×600, all 4 behind it at 360×800 alongside Food) — no new overflow,
  clipping, or wrapping regression from going 2→4 category tabs.
- Selected Household from the overflow sheet: its two real subcategories
  (Cleaning, Personal Care) each render their own header + the translated
  empty-state text cleanly, once each — confirms finding 3's fix (no
  headerless duplicate).
- Enabled the settings-gated Categories tab: the tile grid shows all 4
  categories (previously Household/Produce would have been pruned
  entirely); tapping the Household and Produce tiles each opened the
  picker modal with the same translated message, not a blank popup.
- No console/page errors logged during any of the above.
- Screenshots inspected directly (not just described by the driving
  agent) — confirmed the rendering matches the description before relying
  on it.

Not independently re-run here: the four updated e2e spec files themselves
(Playwright wasn't available for a full suite run in this container this
session; the count/text changes were verified by hand against the real
running app instead, per above, which exercises the identical DOM shape
those specs assert on).

## CI round 2 — real e2e suite failures (not just hand-edited assertions)

Pushing surfaced three genuine `playwright` job failures against the full
e2e suite (not merely the four hand-updated spec files' own new
assertions, which were only reasoned about, not run, before the first
push — a gap this round closed):

1. **`categories-record-dialog-2010.spec.ts` test (g)** and
   **`modifiers-shop-wide-2399.spec.ts`** each create a category with a
   permanently-active item and never deactivate it — harmless before this
   card (a quick-button-only category never showed), but now the leaked
   category renders as a real extra sell-screen tab for the rest of that
   worker's run, intermittently pushing
   `sale-screen-category-strip-overflow-2307.spec.ts`'s demo-tab-count
   assertion and `sale-screen-search-strip-2173.spec.ts`'s
   `CATEGORY_COUNT+5` assertion into overflow. Fixed: both now deactivate
   their probe item in a `finally` block. Root-caused by deterministically
   reproducing the leak (running the leaking test immediately before the
   failing one in one worker) and confirmed fixed across 180+ repeated
   runs — not a viewport/formula tweak, both were correct as written.
2. **`sell-tile-jiggle-done-focus-2417.spec.ts`** left its probe item
   uncategorized (the synthetic "Uncategorized" bucket, always last among
   tabs); with two more real category tabs now in the demo seed, that
   bucket sometimes fell into the tab strip's own overflow-hidden state,
   which hung the test's cleanup POST client-side (confirmed via a
   concurrent `curl` to the same route succeeding in milliseconds — the
   request never left the browser). Fixed: the probe item now uses the
   stable demo "Food" category, which the strip always has comfortable
   room for.

None of these three are bugs in the #2498 fix itself — all are
pre-existing test-isolation gaps this card's change was the first to make
visible, since it's the first thing to turn "a category with an active
item" into "a real, always-rendered sell-screen tab."

## Deferred / follow-up

- New `en.json` key `products.category_no_buttons`: matching PRs merged in
  `ut-plugin-language-de` (#305) and `ut-plugin-language-es` (#305) in this
  same cycle, per `scrum-master/SKILL.md`'s "work with no card" rule —
  landed before this PR merged, since `locale-render-audit` renders live
  German pages and fails on the untranslated string otherwise.
- `make docs-shots` regenerated and committed (sell + till-designer, all 4
  locales) — was NOT just deferred; `guard-docs-shots` is a real build-job
  check and would have failed merge otherwise.

## Verdict

Safe to merge. Gate green (`gofmt`, `go build ./...`, `go test ./...`,
`golangci-lint run ./...` 0 issues, `guard-i18n.sh`, `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
`guard-compliance-claims.sh`, `guard-competitor-naming.sh`) both before and
after the review-round fixes.
