# Review record (IN PROGRESS, not approved): ut-docs#3241 phone tile basket badge

**Status: open findings, do not merge yet.** The build lane stopped this card on its
time budget (`card-budget.sh`: 52 of 45 min) after the Dev, Tester and independent
review phases. The next cycle needs to fix review minors 1–3 and the Tester's e2e
gap below, re-run the gate, and then finish this record (verdict + `Pre-flight:` line).

- Card: universaltill/ut-docs#3241 (complexity:easy). Branch: `feat/3241-phone-tile-basket-badge`.
- Phases run as separate agents: BA, Architect, UX (session model), Dev (Sonnet),
  Tester (session model), Reviewer (Opus, a different model from the author).
- Follow-ups filed: ut-docs#6049 (badges in the category-tiles popup and search
  results), ut-docs#6050 (`.basket-count` contrast in amber/fresh/monarch).

## To do before merge
1. Review minor 1: call the badge apply function after `submitPayments`' raw
   `basket.outerHTML` swap (web/public/app.js ~755), so badges clear after a split-tender sale.
2. Review minor 2: the 768/1024/1280 e2e cases must assert `toHaveCount(1)` before `toBeHidden()`.
3. Review minor 3: put a separator in the translatable string (e.g. `", %d in basket"`), not in JS.
4. Tester gap: the "remove clears" e2e test reloads the page, so it never exercises the
   count-drops-to-0 branch. Remove the line in the sheet on a live page and assert
   `.tile-qty-badge` / `.tile-qty-sr` have count 0.
5. Then run the full gate once (go test ./..., guards, e2e spec, docs-shots guard).

## Dev notes

### Dev notes ut-docs#3241 (branch feat/3241-phone-tile-basket-badge, uncommitted)

Files: internal/pos/service.go (lineItemCount, ItemCountsByItem, ItemCountsJSON; ItemCount reuses lineItemCount),
internal/pos/basket_count_test.go, internal/ui/basket_item_counts_test.go (new), web/ui/partials/basket.html
(data-item-counts + data-in-basket-label on #basket), web/public/app.js (utTileBasketBadges IIFE, afterSettle, coalesced; sr span kept last child),
web/public/app.css (global display:none; phone-block badge, token colours, jiggle hide), web/locales/en.json + web/i18n-meta/en.meta.json
(basket.tile_in_basket), web/help/en/sell.md (UX wording, Strip/All only), e2e/tests/phone-sell-3059.spec.ts (new describe),
web/help/img/manifest.json (regenerated).

TDD red->green: pos tests failed to compile (ItemCountsByItem undefined); ui test failed "missing data-item-counts". Both green after impl.
Deviation: test case "netting to zero" uses +1 and -2 (not -1): the shared ItemCount rule int(qty+0.5) truncates -0.5 to 0, so a -1 line contributes 0.
Kept the shared rule (R3 invariant); pre-existing quirk, not changed.
Render test asserts attribute presence for the label (test T has no locale -> returns the key), not its text.
e2e: ran for real (26/26 pass incl. existing phone-3059 tests) using NODE_PATH shim for @playwright/test (e2e/node_modules absent; /opt/node-tools has `playwright` only).
 Jiggle test toggles the .jiggle-mode class on the grid (CSS check), not a real long-press. Emulated only; no real touch.
docs-shots: make docs-shots ran (132 passed). channel-orders/reports PNGs changed (noise) -> restored from HEAD; manifest kept (surface+sell hash). guard-docs-shots OK.
Gate: gofmt clean, go build, go vet, go test pos/ui/pages ok, golangci-lint 0 issues, guards OK: i18n, i18n-meta, rtl-logical-css, help-topics,
no-showmodal, docs-shots, kiosk-engine, compliance, competitor, fixed-dates, core-neutral, help-drift/terms/breadcrumbs, mail-addresses.
Not done: review record in docs/code-reviews (Reviewer phase); screenshots by Tester for themes/clipping.

## Independent review (Opus)


Scope: `git diff` on feat/3241-phone-tile-basket-badge plus the untracked
internal/ui/basket_item_counts_test.go. This was a read-only review: nothing in the tree was edited.

Guards run locally, all exit 0: guard-docs-shots (surface 0f486f83… is fresh),
guard-i18n, guard-i18n-meta, guard-rtl-logical-css, guard-help-topics.

### Verified OK
- **ItemCount refactor does not change behaviour.** `lineItemCount` matches the old loop body
  (weighed lines count 1, otherwise `int(Qty+0.5)`). The callers are
  internal/pages/index_page.go:192 (payItemCount), basket.html:85 (.basket-count) and
  basket.html:478 (phonebar), plus comment-only mentions in session_manager.go. None of them
  changes behaviour.
- **The JSON is escaped correctly in the attribute.** html/template attribute-escapes it, and the
  Go test pins `{&#34;A&#34;:3}`. `json.Marshal` sorts the keys, so the output is deterministic.
- **The JS holds up under re-renders.** It is idempotent: it reuses an existing badge or sr span
  and removes both at 0, so no badge is ever doubled. It only queries `:scope >`, so it never
  touches the jiggle `.tile-badges` (those are cloned into `.tile-cell`, not into the button). It
  coalesces calls through `setTimeout(0)`. Reading on afterSettle is correct, because the id-matched
  outerHTML settle restores the attributes. Basket swaps, `ut-basket-resync` (htmx.ajax), load-more
  and grid re-renders are all covered. No listener or element leaks.
- **The sr span stays the last child.** It is re-appended after the badge. The badge has
  `pointer-events: none` and `aria-hidden`, so tile clicks and the accessible role are unchanged.
- **The CSS follows the repo rules.** It uses logical properties only and is `display: none`
  outside the phone tier. It uses the token colours --text, --surface. The badge's z-index 1 sits
  below the sticky `.filter-chips` (z-index 2), so it does not paint over the strip when the grid
  scrolls. It is hidden under `.jiggle-mode`, which is the class utTileJiggle really sets on the
  grid (app.js:2660).
- **i18n is complete.** The key is in en.json and has meta (description, review, placeholders).
  JS reads the label from a server-rendered attribute, so there is no hardcoded string in the JS.

### Findings

#### Major
None.

#### Minor
1. **The badges go stale after a split-tender payment.** At web/public/app.js:755 (`submitPayments`),
   the code replaces `#basket` with a raw `basket.outerHTML = text`. That is not an htmx swap, so
   `htmx:afterSettle` never fires and `utTileBasketBadges.apply` does not run.
   - Failure scenario: on a phone, ring up 2× Latte, then pay by split tender. The sale completes
     and the new basket renders with `data-item-counts="{}"`. If the cashier now closes the sheet,
     the Latte tile still shows "2" (and its screen-reader text still says "2 in basket") over an
     empty sale. It corrects itself on the next htmx action, such as New Customer or any tile tap.
   - This breaks the "fresh sale has no badge" intent. The existing `maybePromptAtSaleStart`
     listener has the same blind spot.
   - Fix: expose the apply function (for example `window.UT.applyTileBadges`) and call it after
     line 755, or dispatch a custom event there that the IIFE also listens for.
2. **The e2e tests for 768/1024/1280 can pass without testing anything**
   (e2e/tests/phone-sell-3059.spec.ts, the final `for (const vp …)` block).
   `expect(page.locator('.tile-qty-badge').first()).toBeHidden()` also passes when no badge
   element exists at all, for example if the JS regressed or was scoped out on larger
   viewports. Add `await expect(page.locator('.tile-qty-badge')).toHaveCount(1)` before it, and
   the same for `.tile-qty-sr`, so the test proves the CSS hides a badge that exists.
3. **The accessible name has no separator.** The sr span is appended after the trailing
   whitespace, so the name reads "Latte £2.50 2 in basket". In speech, "2.50 2 in basket" is
   ambiguous. The UX/meta wording promises "Latte, 2 in basket".
   - Options: put the separator into the translatable string (for example `", %d in basket"`,
     with meta noting the leading comma), or add a localized separator key.
   - Do not hardcode the punctuation in the JS (that would break the i18n rule).

#### Nits
4. **The label cannot be pluralised.** `basket.tile_in_basket` is a single-form `%d` string, and
   app.js substitutes only a literal `%d` (`fmt.replace('%d', …)`). Languages with plural
   categories (ar, ru, pl) cannot translate it correctly, and a translator's `%[1]d` would show
   up raw. In English, "N in basket" works for every N. If plurals matter, render a small map of
   plural forms, or note the constraint in the meta description.
5. **One test case only passes because of how Go truncates.** In internal/pos/basket_count_test.go,
   the "netting to zero absent" case relies on `int(-2+0.5) == -1` (truncation toward zero). The
   assertion is fine, but a comment would help, or use `Qty: -1` so the case reads as what it
   means.
6. **The jiggle e2e test fakes the mode.** It toggles `.jiggle-mode` by hand instead of entering
   jiggle mode through a long-press. That is acceptable, because the CSS selector matches the
   class the real code sets.
7. **The badge only appears in the item grid.** The selector requires `.tile-cell > .btn-tile`,
   so search-result tiles get a badge only if they share that wrapper. The help text
   (sell.md: "In the item grid (the Strip or All view)") matches this, so nothing is wrong. It is
   noted only in case search is expected to show badges too.

### Process (repo rules)
- **Language packs:** the new en.json key needs follow-up PRs in the `ut-plugin-language-*` packs
  listed in `scripts/ci/check-lang-pack-drift.sh` PACKS.
- **Review record:** it belongs in `docs/code-reviews/<date>-ut-docs-3241-...md` before the
  commit.
- **Help manifest:** only the `en` hash of the sell topic changed, which is correct (translations
  come through Weblate, and help-drift only warns).

## Tester


Verdict: **PASS with one test gap (non-blocking, should fix)**. No product bug found.
All mutation work was done in a throwaway worktree
(`/tmp/claude-0/-home-user/90c98159-f505-5638-8594-774e50599f7d/scratchpad/3241-wt.P68C/ut`, HEAD + the uncommitted diff);
the real tree was not touched. Manual drive used a separate till on 127.0.0.1:8191 built from that copy
(seed_demo catalogue, auth off, fresh data dir), Chromium headless, deviceScaleFactor 1.

### 1. Are the tests real? (mutation in the copy)

Go (`internal/pos`, `internal/ui`) — baseline green; every mutant went red:
| Mutant | Result |
|---|---|
| M1 don't skip lines with empty ItemID | FAIL `no_item_id_skipped` |
| M2 keep items netting to <= 0 | FAIL `netting_to_zero_absent` |
| M3 `=` instead of `+=` (no summing per item) | FAIL `same_item_two_lines_summed`, `two_variant_lines_one_item` |
| M4 weighed line counted as rounded qty | FAIL `TestBasketItemCount` + `ByItem` |
| M5 template drops `data-item-counts` | FAIL `TestBasketView_RendersItemCountsAttribute` |

e2e (`phone-sell-3059.spec.ts -g in-basket`, project default, 1 worker) — baseline 7/7 green:
| Mutant | Result |
|---|---|
| E1 JS never removes badge/sr when count drops to 0 | **ALL 7 PASS — false pass** |
| E2 drop global `display:none` (badge shows on tablet) | FAIL 768x1024, 1024x600, 1280x800 |
| E3 `inset-inline-end` -> physical `right` | FAIL RTL test |
| E4 drop jiggle-mode hide | FAIL jiggle test |
| E5 SR span first child + no 99+ cap | FAIL tests 1 and 2 |

**Gap (E1):** the test titled "...remove clears" never removes a line on a live page. It does
`/api/pos/reset` and then `page.goto('/')`, so a fresh page has no badge either way. The `else`
branch (`badge.remove(); sr.remove()`) is uncovered. Suggested fix for Dev: in test 1, after the
`-` step back to 2, remove the line in the sheet (✕ or `-` twice), close the sheet, and run
`expect(badge).toHaveCount(0)` plus `expect(firstTile(page).locator('.tile-qty-sr')).toHaveCount(0)`
with no reload. Removing the else branch should then turn the test red.
The behaviour itself is correct: driven live, decrementing the line to 0 removed the badge and the
SR span without a reload, and `lastElementChild` went back to `.tile-price`.

### 2. UX §7 checklist (driven in real Chromium)

- [x] **360x800 and 390x844, Strip mode, 2 taps -> "2" top-right of the icon.** The tile box is
  unchanged before and after (390: 98x162 at 35,138; 360: 88x152). The badge (23x23) sits at
  icon-right+4 and top-4. It overlaps neither the name (y 241) nor the price (y 279).
  Shots: `shots/390-default.png`, `shots/360-default.png`.
- [x] **First row / edge column not clipped.** The script walked the ancestors to the first
  overflow-hidden one: no clipping. The `.products` container is overflow:hidden at x 21–369.
  Right column at 360: no scan tile there in the demo data (Kellogg's opens a picker), so I
  injected `data-item-counts={"itm021":7}` and fired afterSettle. The badge right edge was 329
  against a container right of about 339, not clipped. Shot: `shots/360-rightcol-injected.png`.
- [x] **Themes dark / amber / monochrome at 390, legible.** Computed colour and background equal
  the theme's `--surface`/`--text` in each: dark is light-grey on navy, amber is white on stone-900,
  monochrome is white on black. No raw hex in the new CSS rules (the only `#` matches are
  `#3241` in comments). Shots: `shots/390-dark.png`, `shots/390-amber.png`,
  `shots/390-monochrome.png` (dark and amber viewed: legible, ring visible).
- [x] **fa RTL at 390.** `dir=rtl`. The badge is at the icon's top-LEFT (badge x 253, icon x 257).
  The SR text reads "2 in basket", English because there is no fa key yet, which is expected per
  ADR-0160. `guard-rtl-logical-css` OK. Shot: `shots/390-fa.png`.
- [x] **qty 120 -> "99+".** The badge is 35px wide on a 98px icon. The SR span reads
  "120 in basket" and `getByRole('button',{name:/120 in basket$/})` finds 1.
  Shot: `shots/390-99plus-rightcol.png`.
- [x] **Accessible name order.** `getByRole('button',{name:/2 in basket$/})` finds 1.
  `.tile-qty-sr` is the last child and the badge is `aria-hidden="true"`.
- [~] **Tap on the badge adds an item.** `pointer-events:none` is asserted (the e2e plus computed
  style). I did not do a real tap on the badge pixel or a swipe-scroll with badges present. Not
  tested on real touch hardware.
- [x] **No motion.** `transitionDuration` is `0s` and `animationName` is `none`.
- [~] **Jiggle.** The badge hides when `.jiggle-mode` is on and comes back when it is removed
  (e2e plus mutation E4). This used a class toggle, not a real long-press. I did not look at the
  four-corner edit-badge layout.
- [x] **768x1024, 1024x600, 1280x800: no visible badge or SR span** (0 elements with client rects).
  Shot: `shots/1024x600.png` (viewed; basket and tiles look normal). I did not re-check
  docs-shots PNGs or the existing "tablet layout is unchanged" test myself; Dev reports both green.
- [x] **category_tabs popup and search results show no badge.** In category_tabs mode with an item
  in the basket, the popup has 27 tiles and no visible `.tile-qty-badge` or `.tile-qty-sr`
  anywhere. A search for "Ham" with Ham in the basket shows 2 result tiles and no badge.
  Shots: `shots/390-category-popup.png`, `shots/390-search.png`. `guard-i18n` and
  `guard-i18n-meta` OK.
- [x] **No JS page errors** in any of the drive runs.

### 3. Gate
- go vet pos/ui clean. `go test ./internal/pos ./internal/ui` OK and `./internal/pages` OK (392s). Guards OK: rtl-logical-css, i18n, i18n-meta,
  no-showmodal.
- I did not run the full `go test ./...` or the full e2e suite. I ran only the 7 new tests, plus
  the mutants.

### Not looked at
Real touch or long-press, WebKit lane, German or longest-locale tile names with a badge, light or
dark at 360 (only 390 for themes), kiosk self-order surface (it does not use `.pos-container`
tiles).
