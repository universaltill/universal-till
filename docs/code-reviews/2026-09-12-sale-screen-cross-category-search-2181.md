# 2026-09-12 — Sale screen: search spans every category, not just the active tab (ut-docs#2181)

## What shipped

ut-docs#2173 made search replace the sale screen's category tab strip in
place, on the same row. Filtering semantics were deliberately left
unchanged by that card (`matches()`/`sectionHasMatch()` still filtered
within the active tab only), which combined with the strip being off
screen meant: while search was open the operator couldn't see or change
category, closing search cleared the query (so it couldn't survive a tab
switch), and an item living in a different category than whichever tab
happened to be active returned "No matching products." with no way to
widen the search. The product owner's own reference (SumUp) searches the
whole catalogue, not the active category.

This card reverses that scoping — the exact inverse of the invariant
`e2e/tests/sale-screen-category-tabs-search-418.spec.ts` used to pin
("search never leaks across the active tab boundary"), done deliberately
and in writing (that spec's own header comment now explains the
supersession), not silently:

- **`web/ui/partials/buttons.html`**: a new `panelVisible(id, panelEl)`
  Alpine method — with no query, a tab panel shows only when it's the
  active tab (unchanged); with a query, every panel that has a match
  anywhere inside it (own buttons or nested subcategories) shows at once.
  A new `category-group-body-tabbed` template wraps a top-level tab's own
  buttons in a `category-group` section whose header is shown only while
  a query is active (`x-show="q"`), so a result's category stays
  unambiguous even though the tab bar itself is hidden while searching. A
  nested subcategory already always carried its own header via
  `category-group`, unaffected. The per-panel "no matches" message was
  replaced with a single one for the whole tabbed view (`$el.parentElement`
  now resolves to `#buttons-grid`, spanning every panel, not just one),
  since a query can now show several panels at once. The now-dead
  `category-group-body` template (superseded by `category-group-body-tabbed`
  for the only branch that ever called it) was removed.
- **`internal/ui/buttons_search_visibility_test.go`**: a new
  `TestButtonsHTTPList_TabbedPanelsCarryCrossCategorySearchWiring` pins the
  new markup (`panelVisible(` wiring on every panel, a q-gated header on a
  top-level bucket's own buttons, exactly one whole-view "no matches"
  message).
- **`e2e/tests/sale-screen-category-tabs-search-418.spec.ts`**: rewritten
  to drive the real cross-category behaviour in a browser — this is the
  load-bearing test; see "Independent review" below for why the Go
  template test alone could not have caught the actual bug found in this
  diff.
- **`web/help/{en,de,ar,fa,tr}/sell.md`**: step 1 corrected to describe the
  new behaviour. Real translations weren't available in this session for
  de/ar/fa/tr, so those four got the same untranslated English literal as
  `en/sell.md` (same convention already used elsewhere in this manual for
  an English-only content pass) — follow-up filed as ut-docs#2199.
- `make docs-shots` regenerated (`web/help/img/manifest.json` + a few PNGs)
  — required by `guard-docs-shots.sh`, since the app surface it hashes
  changed. The `sell` topic's own screenshots are byte-identical (the
  screenshot captures the at-rest, non-searching state, which this card
  doesn't change visually); `catalog`'s two changed PNGs are the
  already-tracked, unrelated `<20`-byte docs-shots non-determinism
  (ut-docs#2184), not a real regression.

Follow-ups filed to Backlog rather than fixed in this branch (both
low-severity, real-but-not-urgent edge cases scoped as their own UX
passes): ut-docs#2198 (a cross-category result only shows its *immediate*
category, so two same-named subcategories under different top-level
categories are indistinguishable while searching) and ut-docs#2199 (the
real de/ar/fa/tr translation of the corrected search step).

## Independent review

Opus, fresh context, isolated worktree — full brief in the review
transcript; summarized here.

**What it did, beyond reading the diff:** ran `go build`/`vet`/`test`,
`golangci-lint`, every `guard-*.sh` this diff touches, and the full
targeted e2e suite in a real browser. Then did TDD re-verification twice —
reverted the `panelVisible` method/wiring back to the pre-fix
`x-show="tab === '{{ $g.ID }}'"` shape and confirmed the rewritten e2e spec
failed with the exact symptom the issue describes (a cross-category tile
stuck `hidden`); separately reverted just the `category-group-body-tabbed`
swap and confirmed the category-label assertion failed on its own. Both
reverts were restored and the suite re-confirmed green. This is what
caught the real bug below — the Go template-rendering test cannot see an
Alpine.js runtime reactivity bug, only the e2e spec can, which is why the
review insisted on actually running it in a browser rather than trusting
the diff's own claims.

**Real bug found and fixed in this branch**: the first cut of
`panelVisible(id)` read `this.$el` *inside* the method to look its own
panel up by id (`this.$el.querySelector('#cat-panel-' + id)`). Alpine's
`$el` magic resolves to whichever element the *current* expression is
bound to — here, the panel's own `x-show` — so `this.$el` was the panel
itself, and `querySelector` only searches *descendants*, never the element
it's called on. The panel could therefore never find itself, so
`panelVisible` silently fell through to `false` the instant a query made
it take that branch. Caught live: the panel's own inline `style` stayed
`display:none` after a query started matching, confirmed by walking the
Alpine reactive-data object and the DOM's actual computed style side by
side in a real page. Fixed by passing `$el` in explicitly from the
template (`panelVisible('{{ $g.ID }}', $el)`), matching the
`sectionHasMatch($el)` convention already used everywhere else in this
file — the method now takes the element as data instead of re-deriving it
from a magic property whose scope doesn't match what the author assumed.

**Other findings, triaged:**

- **`guard-docs-shots.sh` red** (blocker) — fixed: `make docs-shots` run,
  see "What shipped" above.
- **Four manual locales left describing the removed behaviour** (high) —
  fixed as described above; real translation deferred to ut-docs#2199 with
  the English literal standing in, per this manual's existing convention
  for that gap.
- **WAI-ARIA tabs pattern broken while a query is active** — with the
  tablist hidden and more than one panel visible at once, panels kept
  `role="tabpanel"`/`aria-labelledby` pointing at a tab that no longer
  describes an exclusively-selected panel. Fixed: `:role`/`:aria-labelledby`
  now drop to a plain `group` (no forced labelledby) while `q` is truthy,
  and restore the real tab semantics the moment the query clears —
  verified with a new e2e assertion (see the spec's own F3 comment).
- **Dead `category-group-body` template + four comments now describing the
  wrong template/location** (`buttons.html`'s own tab-bar comment,
  `product-tile-category-color-1325.spec.ts`, `buttons_http_test.go`, and
  the pre-existing ut-docs#422 test's own doc comment + failure strings) —
  fixed: dead template removed, every stale comment corrected to name
  `category-group-body-tabbed` and the message's new whole-view scope.
- **Ambiguous category label when two subcategories share a name across
  different top-level categories** — real gap, deferred to ut-docs#2198 as
  its own UX-scoped follow-up rather than folded into this diff.
- **Help text overstated the label as "another category" only** — fixed
  in `en/sell.md` (every match gets a label while searching, not only a
  cross-category one).
- Comment/signature mismatches (`panelVisible(id)` missing its second
  argument in two comments) — fixed.

**Checked adversarially and found clean** (verified live, not just
reasoned about): every other Alpine `$el`/magic-property use in this file
resolves correctly; no "both a matching panel visible and the no-matches
message visible" contradiction is reachable (traced + measured with
several queries); zero network requests fire from opening search or
typing a cross-category query (offline-first intact); a cross-category hit
is actually clickable end-to-end (added to the basket from a tab other
than the one currently active); the synthetic "uncategorized" bucket
coexists correctly with `$hasTabs`; no visual regression to the at-rest
tab view from the new wrapper section; no new i18n keys, no locale drift,
RTL unaffected; no real client/shop name or secret-shaped literal in the
diff.

## Verified beyond automated tests

- Real browser run (not just the Go template-rendering tests) of the
  rewritten `sale-screen-category-tabs-search-418.spec.ts`,
  `sale-screen-search-strip-2173.spec.ts`, `product-tile-category-color-1325.spec.ts`,
  `tab-bar-overflow-aria-424.spec.ts`, plus a regression sweep of every
  other spec touching this markup (designer reorder/search, codeless-item
  shortcut) — 28 passed, 0 failed, 0 console errors.
- TDD re-verification of the core fix, twice over (see "Independent
  review" above) — the rewritten e2e spec is a genuine regression test for
  both halves of the fix, not a false pass.
- `make docs-shots` run for real (not skipped) and `guard-docs-shots.sh`
  re-confirmed green afterward.

## Safe to merge

Yes. One real runtime bug was found and fixed before merge (the `$el`
scoping issue above) — the Go-only test suite this diff also added would
not have caught it on its own; only the real-browser e2e run did. All
review findings were either fixed in this branch or deliberately deferred
to a filed follow-up card, per severity.
