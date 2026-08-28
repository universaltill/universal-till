# Code review — sale screen: products-dominant layout (ut-docs#1231)

- **Date:** 2026-08-28
- **Branch:** `fix/1231-sale-screen-products-dominant-layout`
- **Reviews:** two independent rounds (Opus, isolated worktrees, different
  context from the Sonnet implementer each time), both round 1's blocker and
  round 2's blocker addressed before this record was written.
- **Verdict: safe to merge.**

This record supersedes the two rounds' own interim files as the record of
what actually shipped — read this one, not an earlier draft.

## The bug (live report, product owner, Pi5-1, v0.6.12)

At the reference till (1280×800 logical), with exactly one configured
product, the PRODUCTS panel got only ~280px total — the first product tile
rendered clipped mid-tile, reading as "the button isn't there" — while the
payment area (Card/Cash/Gift Card/Hold Sale/New Customer) permanently took
roughly half the vertical space, even with an empty basket.

Root cause: `.pos-container`'s `grid-template-rows` was `minmax(8rem, 1fr)
minmax(0, auto)` (products, tender). A CSS Grid `auto` max track is
maximized to its own max-content size BEFORE an `fr` track gets any leftover
space, so tender — sized to fit its own content with no scrolling — always
won the fight for height first, leaving products whatever was left over.

## What shipped, and how the fix got here (three iterations, two review
rounds — the failure modes at each step are the useful part)

**Iteration 1** — `minmax(8rem, 2.4fr) minmax(0, 1fr)`: made both rows `fr`
tracks weighted 2.4:1 toward products, keeping products' original 8rem
floor and giving tender no floor at all. **Round 1 review**, measuring in a
real browser rather than trusting the diff's own claims, found this only
*relocated* the bug: tender's floor of 0 let Cash/Card/Gift Card/Hold Sale/
New Customer shrink to nothing with zero visible affordance they existed —
worse than products' own clipping, since a clipped tile at least reads as
"partially there."

**Iteration 2** — `minmax(21.5rem, 2.4fr) minmax(12.5rem, 1fr)`, tuned and
verified at exactly 1280×800 to guarantee BOTH a full tile row and a full
Cash/Card row with zero scrolling, plus two `@media` fallbacks (short
viewport, OSK open) reverting to the original 8rem/6rem pair. **Round 2
review**, again measuring for real rather than trusting round 1's own
comments, found this worked only in a ~30px-tall band around 1280×800: at
1366×768, 1280×768, and other extremely common real display heights, the
combined 34rem floor exceeded `.pos-container`'s own available space — and
because the container had no `overflow` of its own and the fixed status-bar
footer sits underneath it, the excess didn't scroll, it silently rendered
UNDER the footer. `document.elementFromPoint` at Cash's on-screen
coordinates resolved to `FOOTER.statusbar`, not the button — worse than
iteration 1 and worse than the pre-#1231 baseline, at those heights.

**Iteration 3 (shipped)** — the conclusion both rounds converge on:
guaranteeing "products shows a full tile row" AND "tender shows a full
Cash/Card row" with literally zero scrolling anywhere is not achievable
with a fixed floor pair. The two needs together want ~34rem, and the
shortest *common* real desktop height (768px logical) doesn't have room for
that at any font scale this product ships — that's arithmetic, not a tuning
miss. So the fix stops trying to buy tender a zero-scroll guarantee at the
cost of fragility everywhere else:

```css
grid-template-rows: minmax(12rem, 2.4fr) minmax(8rem, 1fr);
```

modest floors, small enough to fit safely across the realistic viewport
range (1024×600 through 1920×1200+) with real margin, plus the same 2.4:1
`fr` weighting toward products for whatever's left over. Products reliably
keeps its first tile row uncut at comfortable viewports (1280×800, 1280×780,
1280×768, 1366×768 all measured fully un-clipped) and never collapses
toward zero at a squeezed one — the *original*, reported bug, now fixed.
Tender keeps a real, non-zero floor (never invisible again — round 1's
finding) but is no longer guaranteed scroll-free everywhere; `.tab-panel`'s
own 6rem floor plus the panel-level `overflow-y: auto` keep every payment
button reachable by scroll at any viewport this product ships (unchanged
mechanism, verified again below). `.pos-container` itself also gains
`overflow-y: auto` as a structural safety net — if some future change ever
makes even these modest floors not fit somewhere, the failure mode is a
scrollable sale screen, not silent content behind a fixed footer (the exact
class of failure round 2 found). The two `@media` fallbacks from iteration
2 are no longer needed and were removed — the base floors are already safe
without them, which also means one less place for the two rules to
interact.

**Explicitly out of scope, deferred**: making Cash/Card fully visible with
*zero* scroll at the reference till specifically needs the tender panel's
own footprint actually redesigned (compact/collapsed until the basket has
items, expanding at pay time) — this was this card's own original AC2,
marked "Design decision for UX role" from the start. Two rounds of review
now provide the evidence that a grid-track number cannot deliver that
guarantee robustly; the redesign is real, separate UX work, not a follow-up
oversight.

Also shipped:

- **`web/public/osk.js`** — `hide()` (closes the on-screen keyboard,
  called synchronously from a `focusin` handler) used to synchronously
  remove `osk-open`/`osk-padded`. Since iteration 1 made the row split
  respond much more to available space, closing the OSK now visibly moves
  the tender panel's controls — including the scan-row's Add button —
  between an operator's touch-down and touch-up (`focusin`→`hide()` fires
  from the SAME gesture's `mousedown`, before the browser resolves that
  gesture's `mouseup`/`click`). Fixed by deferring the two layout-affecting
  `classList.remove` calls to `requestAnimationFrame`, landing after the
  current gesture resolves; `current = null` stays synchronous. Round 1
  additionally found the deferred removal could be overtaken by a `show()`
  landing in the same frame (pointerdown on a plain focusable element
  fires `focusin`→`hide()`, and the same gesture's `pointerup` can still
  land on a `[data-osk-toggle]`, re-showing synchronously) — leaving the
  keyboard hidden while `current` says it's open. Fixed with a guard:
  `if (current) return;` inside the deferred callback — `current` is the
  one thing that can legitimately change before the frame completes, and a
  live `current` means the keyboard is wanted, so there's nothing to
  close.
- **`e2e/tests/sale-screen-213.spec.ts`** — two new regression tests:
  `products grid gets the dominant share and the first tile is never
  clipped (ut-docs#1231)` (products height > tender height, first tile
  fully within the products panel, at 1280×800) and `the scan-row Add
  button stays above the OSK, not squeezed under it (ut-docs#1231)` (opens
  the real OSK, asserts the Add button's box is on-screen and is the real
  `elementFromPoint` hit target — the regression round 2's own fix
  introduced and this iteration's simpler design fixes without a
  special-case rule). `afterEach` now also resets OSK mode to `auto`
  (round 2's G5: the OSK test's own cleanup at the end of its body never
  runs if an earlier assertion in it fails, leaking `osk=on` into every
  later spec on the shared server).
- Manual screenshots (`web/help/img/{ar,en,fa,tr}/sell.png`,
  `manifest.json`) regenerated via `make docs-shots` to match the final
  layout — captured at the documented kiosk viewport, they now show
  products dominant and un-clipped, with a visible sliver of the Pay
  button as a real scroll affordance (not literally absent, as iteration
  2's screenshot showed — the same "the screenshot IS the evidence" check
  that caught round 2's own regression).

## Independent measurement, done for real (this iteration, not taken on
faith from the diff's own comments)

Real Chromium (`/opt/pw-browsers/chromium-1194`), real served till, no
`scrollIntoViewIfNeeded()` for the "does it clip/hide" checks (that call is
exactly the blind spot `tender-panel-reachable.spec.ts` has — round 1's own
finding):

| Viewport | products tile clipped? | `.pos-container` overflows into footer? | Cash reachable via scroll? |
|---|---|---|---|
| 1280×800 | no (fully visible) | no | yes, real hit-test target |
| 1280×780 | no | no | yes |
| 1280×768 | no | no | yes |
| 1366×768 | no | no | yes |
| 1280×740 | small (~1px) | no | yes |
| 1280×720 | small (~12px) | no | yes |
| 1280×701 | small (~22px) | no | yes |
| 1280×700 | small (~23px) | no | yes |
| 1024×600 | ~93px (no worse than `main`'s own baseline there — `main` also clips at 1024×600, per round 2's own comparison table) | no | yes |

OSK open at 1280×800: `.pos-container` bottom = tender bottom = 488.25/
488.27 (no overflow), Add button box fully on-screen, `elementFromPoint` at
its center resolves to the real button. No special-case CSS needed for
this — the modest base floors plus the new `overflow-y: auto` safety net
handle it without the two `@media` fallbacks iteration 2 needed.

## Gates (run for real, this iteration's final state)

- `gofmt -l .` → clean. `go build ./...` → clean. `go vet ./...` → clean.
- `go test ./...` → all packages `ok`.
- All 18 CI-blocking guards in `.github/workflows/ci.yml`'s current
  `build:` job → all pass, including `guard-docs-shots.sh` (surface hash
  `41790f561d65…`), `guard-i18n.sh`, `guard-osk-loaded.sh`,
  `guard-help-topics.sh`.
- e2e, real Chromium: `sale-screen-213.spec.ts tender-panel-reachable.spec.ts
  sale-screen-osk-scan-submit-1177.spec.ts settings-osk.spec.ts
  ui-scale-basket.spec.ts` → **24/24 passed**. Full `--project=default` →
  **201 passed, 2 failed** (`catalog-image-to-till.spec.ts` — an image-load
  timing issue reproduced identically on unmodified `main`, confirmed by
  `git checkout 23d43b2 -- web/public/app.css web/public/osk.js` and
  re-running just that spec; `split-tender-i18n-925.spec.ts` — passes in
  isolation, fails only when run after other specs share its server-side
  state, a pre-existing cross-spec-state issue this diff doesn't touch).
  Both are the same pre-existing/environmental failures both review rounds
  independently cross-checked against `main`.

## TDD re-verification (done personally, both new tests)

Reverted `grid-template-rows` to the true pre-#1231 value
(`minmax(8rem, 1fr) minmax(0, auto)`), left the new tests and `osk.js` as
shipped:

```
✘ products grid gets the dominant share and the first tile is never clipped (ut-docs#1231)
Error: products must get the dominant share of .pos-container —
       got products=181.671875 tender=410.25
```

Matches the exact numbers both review rounds independently measured on the
same baseline. The OSK/Add-button test stayed green against the true
original baseline (expected — that specific regression was introduced by
this card's own iteration 2, not present pre-#1231; it's a guard against
this card's own mistake, not the original bug). Restoring the fix: **both
tests pass**, verified after the revert and again after the final CSS
simplification (iteration 3).

## Secrets / PII / demo data

Clean — no credential-shaped literal, no real shop/client name. Screenshots
show the seeded demo shop and catalogue only.

## Recurring bug classes this pipeline watches for

Neither applies: no new file-write handler (no `os.MkdirAll` gap possible),
no new path construction (no cwd-relative/`paths.Data` gap possible) — this
diff is CSS, client JS, a test file, and regenerated screenshots.

## Manual / help topic

`web/help/{en,ar,fa,tr}/sell.md` describe no step this diff changes — this
is a pure layout/visual fix, not a workflow change, so only the screenshot
needed regenerating (done, verified above).
