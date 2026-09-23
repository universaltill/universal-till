# Code review: sell-screen category strip overflow (ut-docs#2307)

**Date:** 2026-09-21
**Card:** ut-docs#2307 — "Sell screen category strip must not scroll — show
what fits plus a '…' button that opens a sheet of all categories"
(product owner, direct: *"I prefer to have a ... button at the right; when
clicked it shows all the categories to select"*)
**Complexity:** medium — Sonnet built, Opus reviewed (one round)

## What shipped

Frontend-only. The sale screen's category strip (`.products-finder
.tab-bar`) stops being a horizontally-scrolling row and becomes a measured
fit-or-overflow one.

- `web/ui/partials/buttons.html` — new Alpine methods on the
  `.products-finder` `x-data`: `applyCategoryOverflow()` measures the row
  and hides whichever per-category tabs (`[data-cat-tab]`, a new marker so
  the fixed Categories/All tabs are never touched) don't fit, revealing a
  trailing `#cat-tab-more` "…" button only once at least one genuinely
  doesn't; `setupCategoryOverflow()` runs it on mount and wires a
  `ResizeObserver` on the bar itself (not `window` — the ut-docs#2308
  divider drag resizes this box with no window resize event);
  `promoteCategoryTab()`/`selectCategoryFromOverflow()` move a chosen
  category's real, already-bound tab button to the front of the strip by
  plain DOM reordering; `openCategoryOverflow()`/`closeCategoryOverflow()`/
  `trapOverflowTab()` drive a new non-modal `<dialog>` sheet listing every
  category. `focusTab()` now filters `!t.hidden`. The per-tab
  `$el.scrollIntoView(...)` calls are gone (nothing scrolls any more).
- `web/public/app.css` — `overflow-x: auto` and the whole scroll-fade
  `::before`/`::after` recipe removed; new `.tab-more` and
  `.category-overflow-dialog`/`-grid`/`-tile`/`-count` rules.
- `web/public/app.js` — `window.utTabBarFade` kept (catalog.html still uses
  it) with its comment corrected to one caller.
- `web/ui/pages/index.html` — the now-dead sale-screen scroll-fade wiring
  removed.
- `internal/httpx/icons.go` — one new `ellipsis` rail icon (Lucide,
  unmodified).
- `web/locales/{en,ar,fa,tr}.json` — one new key, `products.more_categories`.
- `web/help/en/sell.md` — step 1's paragraph rewritten for the new behaviour.
- e2e: new `sale-screen-category-strip-overflow-2307.spec.ts`; updated
  `sale-screen-search-strip-2173.spec.ts` and
  `tab-bar-overflow-aria-424.spec.ts`.

## Independent review

Opus subagent, fresh context, deliberately a different model from the Sonnet
that wrote the diff. Reviewed against the surrounding untouched code (the
whole `.products-finder` `x-data` block, the generic `.tab-bar` rules, the
`category_filter_popover.html` / `openCategoryPicker` precedents), not just
the added lines.

**Verdict: safe to merge** after two real bugs found and fixed here. Both
were in `applyCategoryOverflow()`'s resize path — the path the card's own
`ResizeObserver` exists for — and neither was covered by the spec as
submitted. Both were found by driving a real browser, not by reading.

### M1 — re-widening never restored hidden tabs (real, fixed)

`naturalWidth()` read `el.getBoundingClientRect().width` for any visible tab
and cached it in `el.dataset.utNatW`. But `.tab-bar .tab` is `flex: 1 1 0%`
(app.css), so a *visible* tab is stretched to its share of the row's slack —
and how much slack there is depends on how many siblings the **previous**
pass hid. The measurement therefore fed on its own output and ratcheted one
way only: once the strip narrowed and hid tabs, the survivors each measured
wider, so widening back never brought the hidden ones back. Caching couldn't
fix it either — the first measurement is already a stretched one.

Reproduced live at 1600px → 620px → 1600px: **2** category tabs showing
where a fresh load at that same width showed **4**, permanent until a
reload. The ut-docs#2308 divider drag this observer exists to follow walks
exactly that path, so a cashier dragging the basket divider narrower and
back loses category tabs for the rest of the shift.

Fixed: the row is now measured in one deliberate pass with every category
tab un-hidden, so the whole set overflows, nothing has slack to be stretched
by, and each tab reports its own min-content floor (`white-space: nowrap`).
A `.ut-measuring` class switching `flex-grow` off for the same instant is
kept as belt-and-braces so the pass is a content-width measurement
unconditionally rather than only on the branch that reaches it — checked
both ways, and the comments say which part is the fix (the earlier draft of
my own comment overstated the CSS rule's role; corrected).

### M2 — a narrowing resize could hide the SELECTED tab (real, fixed)

Tabs were hidden purely by DOM order (`i >= fitCount`), with no regard for
which one was active. Selecting the 4th tab at 1600px then narrowing to
620px got three things wrong at once, all confirmed in the browser:

- the strip showed **no active tab at all** while its panel still showed
  that category's items;
- the roving tabindex puts the only `tabindex="0"` on the selected tab, so
  hiding it left the **whole tablist with no keyboard stop** — the trailing
  "…" button, a plain button outside the tablist, became the only
  Tab-reachable control in the row;
- focus sitting on that tab fell back to `<body>`, where the strip's
  arrow-key handler can no longer see it (it is bound on `.tab-bar`, which
  no longer contains `document.activeElement`), so `focusTab()` never runs
  again and arrow keys do nothing.

This is the specific case the `focusTab()` `!t.hidden` filter *looks* like
it handles but doesn't: the filter fixes the `indexOf` arithmetic, not the
lost focus. Nothing was ever unreachable — the sheet always lists every
category — but the strip was left in a state with no selection shown and no
keyboard way back into it.

Fixed: `applyCategoryOverflow()` now promotes the selected tab into the
fitting set, then re-fits against the new order (promotion permutes the
widths, so the count has to be recomputed, not reused). This is exactly what
`selectCategoryFromOverflow()` already did for the sheet path, so both paths
now agree that the category you picked keeps its spot. It only fires when
the alternative is that tab vanishing, so a resize that changes nothing
never reorders the row.

### L1 — `categories.item_count` reuse is semantically inconsistent (accepted, backlog)

The sheet's per-tile count is
`{{ printf (T "categories.item_count") (len $g.Buttons) }}`. `.Groups[].Buttons`
are **quick buttons** (`BuildCategoryGroups`, `internal/ui/buttons.go`), but
the only other user of that key, `web/ui/pages/categories.html`, feeds it
`.ItemCount` — the real catalog item count. The same string, "%d item(s)",
can therefore show two different numbers for the same category on two
screens (20 on the Categories admin page, 3 in this sheet).

Accepted as-is, not fixed: the count shown here is genuinely what the
operator will see under that tab, and the card's own explicit constraint is
that this sheet adds **no new query** — so the only real fix is a distinct,
more precise key (e.g. `products.category_button_count`, "%d button(s)"),
which is a product-copy decision plus four locale translations and a
lang-pack follow-up. **Suggested backlog card**, not scope-creeped into this
diff.

### Checked and found correct (no change needed)

- **RTL.** No physical `left`/`right` anywhere in the new CSS — the only
  matches in the added lines are prose in comments and the pre-existing
  `@keydown.left`/`.right` Alpine key names, which already route through the
  `direction === 'rtl'` ternary. `.tab-more` uses `margin-inline-start`, the
  dialog uses `inset-inline`/`margin-inline`/`inset-block-start`. Verified in
  a real `fa` render: the "…" lands at the logical end of the row (screen
  left under RTL) and the strip still never scrolls.
- **i18n.** `products.more_categories` is present in all four locales with
  real translations, not English copies (ar "المزيد من الفئات", fa
  "دسته‌بندی‌های بیشتر", tr "Daha fazla kategori"). The dialog heading reuses
  `products.categories` and the close button `common.close` — both correct
  reuses, same meaning in both contexts. No un-keyed user-facing string in
  the new markup. `guard-i18n.sh` clean.
- **Help text.** Read `web/help/en/sell.md`'s new paragraph against the
  actual UI rather than the diff. Every claim holds: the "…" appears at the
  strip's end only once something doesn't fit, nothing changes if they all
  fit, the sheet lists every category with its colour (`--cat-color` via
  `border-inline-start`) and its count, the current one is marked
  (`.active` ring + `aria-current`), and picking one gives its tab a spot in
  the strip. The M2 fix strengthens that last promise rather than
  contradicting it, so no help edit was needed for it.
- **Overflow-count correctness.** No category is ever both hidden and
  unreachable — the sheet renders `range .Groups` unconditionally,
  independent of the fit computation. Zero categories: `!catTabs.length`
  early-returns with the "…" hidden. Exactly one category with no All tab:
  `$hasTabs` is false, so the bar never renders and `$refs.tabBar` is
  undefined, early-returned. The fixed Categories/All tabs are excluded from
  hiding by the `[data-cat-tab]` marker and counted as fixed width.
- **ResizeObserver lifecycle across htmx swaps.** The `x-data` root is
  inside the `.products` div that `buttons-changed`/`modifiers-changed`
  outerHTML-swaps, so the whole component is rebuilt and `x-init` re-runs
  against the **new** bar — no stale `$refs`, no double-observation of one
  element, and the old (detached, unreferenced) observer is collectable.
  Verified empirically: after firing `buttons-changed`, the post-swap bar
  still re-fits on narrow **and** on re-widen.
- **Accessibility.** The sheet is reachable and operable by keyboard alone
  (Tab to "…", Enter opens, focus lands on Close, Tab cycles inside the
  trap, Escape closes and returns focus to the trigger) — all asserted in
  the spec and re-run here. `aria-haspopup="dialog"` + `aria-expanded` on
  the trigger; the "…" is deliberately not `role="tab"`, so it stays out of
  the tablist's roving set. No `aria-modal` — correct, since `.show()` is
  deliberately non-modal so the till's on-screen keyboard stays reachable.
- **The two recurring bug classes this pipeline keeps finding.** Neither
  applies: the entire diff (including my fixes) contains **zero** file-I/O
  call sites — no `os.MkdirAll`/`os.WriteFile`/`filepath.Join`/`paths.Data`
  — the only Go change being one map-literal entry in `icons.go`. Confirmed
  by grep over the full diff rather than assumed from "it's frontend-only".
- **Test data.** No real shop or client names. Fixtures are synthetic and
  run-tagged (`Ovf2307 <tag> <run> NN`, `Strip2173 <tag> Cat NN`), and the
  new spec deactivates every item it creates so the shared worker till's
  strip is restored for the next spec file.
- **The `.category-overflow-tile` vs `.category-tile` split** the author
  called out is a real collision correctly avoided —
  `sell-screen-categories-tab-2283.spec.ts`'s own `.category-tile` locator
  would otherwise have resolved to this sheet's tiles; that spec passes.

## TDD sanity check

This is a new feature, not a bug fix, so rather than a literal revert: the
four implementation files (`buttons.html`, `app.css`, `app.js`,
`index.html`) were checked out from `origin/main` while keeping the new and
updated test files, then restored (md5-verified byte-identical afterwards).

Against the old code all three new tests **fail for the right reasons** —
not merely "element not found", but the real behavioural assertion:

```
Error: tab-bar must never overflow — it is clipped, not scrolled
expect(received).toBeLessThanOrEqual(expected)
Expected: <= 1
Received:    3341
```

plus `#cat-tab-more` genuinely absent in tests (b) and (c). Restored, all
three pass. The new test is not a false pass.

The same check was applied to my own two fixes via a new test (d),
`(d) resizing the strip: re-widening restores tabs, and the selected tab is
never the one hidden`. Each half was isolated and confirmed load-bearing:

- with only the M2 (promote) fix missing → fails at "the selected tab must
  not be the one hidden" (`Expected: false, Received: true`);
- with only the M1 (measurement) fix missing → fails at "re-widening
  restores the hidden tabs" (`Expected: 4, Received: 1`).

## What was verified beyond the automated tests

- **Driven browser runs**, not just assertions: instrumented probes reading
  live `getBoundingClientRect`, `hidden`, `scrollWidth - clientWidth`,
  `document.activeElement` and computed `flex` across 620/700/900/1600px,
  comparing every resize result against a fresh page load at the same width.
  Post-fix they match exactly at every width (620→2/2, 900→4/4, 1600→4/4,
  700→3/3, 1600→4/4) with `overflowPx` 0 throughout. This is how M1 and M2
  were found; neither is visible by reading the diff, and my first
  hypothesis about M1 was in fact *disproven* by the first probe before a
  second, discriminating one confirmed the real mechanism.
- **Manual read of the help topic** against the real UI (above).
- **RTL** checked as a real `fa` render, not by grepping for `left`/`right`
  alone.
- Console was clean throughout (`watchConsole`/`assertClean` in every spec),
  so the un-hide/re-hide measurement pass introduces no `ResizeObserver loop
  completed with undelivered notifications` error.

## Local gate

All green in this reviewer's own run, re-run in full after the fixes:

- `gofmt -l .` (no output), `go build ./...`, `go vet ./...`,
  `go test ./...` (all packages ok), `golangci-lint run ./...` (**0 issues**).
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-compliance-claims.sh` — all pass.
- e2e, Chromium: `sale-screen-category-strip-overflow-2307`,
  `sale-screen-search-strip-2173`, `tab-bar-overflow-aria-424`,
  `sale-screen-category-tabs-search-418`, `sell-screen-categories-tab-2283`,
  `rtl` — **26 passed** (25 as submitted, plus the new regression test (d)).
- `shellcheck` is not installed in this review container; the diff touches no
  shell script, so nothing to check.

## Known, expected gap: `guard-docs-shots.sh`

`guard-docs-shots.sh` **fails on this branch**, for exactly two reasons and
no others:

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go) changed ...
guard-docs-shots: topic markdown changed since its screenshot was taken (locale/topic):
  - en/sell
```

Both are the direct, expected consequence of this card, and the sell screen
genuinely changed pixels (the "…" button, the removed scroll fade), so this
is a real regeneration, not a case for
`update-docs-shots-surface-hash.sh`. My own fixes touch the same two trees,
so they keep the same guard failing for the same reason — no new category of
failure.

The reviewer deliberately did not run `make docs-shots` themselves, for the
reasons above (a large, hard-to-review binary diff, and font-stack risk in
an unverified environment).

**Closed by the orchestrator (Sonnet), same session, same container every
phase of this cycle ran in** — not a separate/unverified environment, so
the font-stack objection above doesn't apply here. Ran `make docs-shots`
for real (`e2e/scripts/docs-shots.sh`, pre-installed Chromium,
`~2.2m`, 124/124 screenshots passed); it touched exactly `web/help/img/
{ar,en,fa}/sell.png` and `manifest.json` — no other topic's image changed,
confirming the scope this review predicted. `guard-docs-shots.sh` now
passes clean:
`✓ docs-shots guard: 31 routed topics × 4 locales screenshotted and fresh`.
Full local gate (build/vet/test/lint/all guards/26 e2e) re-run once more
after this and stayed green. This was the one remaining gap; none left.

## Follow-ups suggested (not filed by this cycle)

1. **`categories.item_count` means two different things** (L1 above). A
   distinct key for the sell-screen sheet's quick-button count, plus ar/fa/tr
   values and the `ut-plugin-language-{de,es}` follow-up.
2. `trapOverflowTab()`'s focusable selector includes a bare `[tabindex]`,
   which would also match `tabindex="-1"` elements. Harmless today (the sheet
   contains only buttons) and inherited from `record-dialog.js`'s own
   long-standing pattern, but both copies would be better filtering
   `:not([tabindex="-1"])`.
3. Focus can still be lost if the operator arrow-keys focus onto a
   *non-selected* tab (arrowing focuses without selecting) and a resize then
   hides that one. Much rarer than M2 and not a dead end — the "…" button
   remains Tab-reachable and the tablist keeps its stop on the selected tab —
   so left alone rather than adding more focus bookkeeping to this card.

## Related

ut-docs#2173/#993 (the single-row strip this replaces the scrolling of),
ut-docs#2212 (the All tab), ut-docs#2283 (the optional Categories tab and its
`.category-tile` picker), ut-docs#2294 (server-side search, the All grid),
ut-docs#2308 (the basket/products divider drag that resizes this strip),
ut-docs#424 (the WAI-ARIA tabs pattern the strip implements).
