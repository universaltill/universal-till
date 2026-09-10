# Catalog item-form tab strip: scroll-shadow affordance (ut-docs#2024)

## What shipped

At 360px the catalog item-form's tab strip (`.catalog-form-head .tab-bar`,
ut-docs#2000) is a single horizontally-scrollable row — five tabs need
~540px in ~328px available, so one or two sit entirely off-screen with no
visual hint they're there (independent review of #2000, finding F5).

Fix, scoped to the existing `@media (max-width: 700px)` block:

- `web/public/app.css` — two `position: sticky` pseudo-elements
  (`.tab-bar::before`/`::after`), flex children of `.tab-bar` itself (the
  scrolling element), positioned via `inset-inline-start`/
  `inset-inline-end` so the browser's own bidi resolution puts each on the
  correct physical edge for both LTR and RTL. A `.tab-bar--fade-start`/
  `.tab-bar--fade-end` class pair toggles their opacity. Because
  `background-image` gradients have no reliable logical direction keyword
  across engines, an `html[dir="rtl"]` override flips only the gradient's
  *internal* colour direction — not the elements' position, which the
  logical inset properties already handle for free.
- `web/ui/pages/catalog.html` — `tabBarFade(el)` computes
  `Math.abs(el.scrollLeft)` against `el.scrollWidth - el.clientWidth` and
  toggles those two classes; wired to a `scroll` listener on the tab bar
  and to the existing `openModal()` → `afterTabPaint()` callback (so the
  first paint is measured after the dialog is actually laid out, not while
  it's still `display: none`).
- `e2e/tests/catalog-tab-strip-fade-2024.spec.ts` — new Playwright spec:
  LTR fade toggling (at-rest vs. scrolled-to-end), RTL (`fa`) fade
  toggling under the negative `scrollLeft` convention, and a 1024×600
  kiosk-floor check that the fade never appears (the strip wraps instead
  of scrolling there, unaffected).

## Why `Math.abs(scrollLeft)`, specifically

Chromium/Firefox/Safari all use `scrollLeft` 0 → +max for LTR, but the
*mirrored* 0 → −max for RTL (verified live at 360px, `dir="fa"`:
`el.scrollLeft` ranges from `0` to `-(scrollWidth-clientWidth)`, never
positive). A first CSS-only attempt at this fix (pure
`background-attachment: local`/`scroll` layers, no JS) was tried and
**rejected** after it was verified — not just reasoned about — to be
backwards under RTL: the fade showed up at the wrong scroll position
entirely, not merely on the unmirrored side, because `local` attachment's
percentage positions tie to the scrollable content's own coordinate
origin, and that origin sits at a different physical edge once
`direction: rtl` flips which edge is the reading start. Driving `tabBarFade()`
off real scroll arithmetic with `Math.abs()` sidesteps that origin
ambiguity entirely, and the CSS's only remaining RTL-specific rule is the
purely cosmetic gradient-direction flip.

## Independent review

A fresh-context Sonnet subagent (per Model Routing for `complexity:easy`,
the card's own size), isolated in its own git worktree, reviewed the diff
independently.

- **No blocking findings.**
- Two non-blocking findings, both deliberately deferred rather than fixed
  here:
  1. `tabBarFade()` runs on `scroll` and once on dialog open, but not on
     `resize`/`orientationchange`. If the dialog stays open across a
     viewport-width change that crosses the 700px breakpoint (a kiosk
     tablet rotated mid-edit, a desktop window resize), the fade classes
     go stale until the next scroll or reopen. Real but narrow, and the
     codebase has no existing `resize`/`ResizeObserver` convention
     anywhere in `web/` to match, so this isn't a deviation from
     established practice — filed as ut-docs#2032 rather than widening
     this card.
  2. A ~2px dead zone in the `pos > 1` / `pos < max - 1` thresholds where,
     for a hypothetically tiny `max` (≈2px), neither fade class would be
     set. Cosmetically invisible given tabs are far larger than 2px —
     accepted, not filed.
- The reviewer independently re-verified the TDD claim by reverting only
  `app.css`/`catalog.html` (the spec left in place) inside its own
  worktree: 2 of the 3 new tests failed with the exact assertion mismatch
  the spec's own comments predict (`getComputedStyle` returning the CSS
  initial `opacity: 1` for a pseudo-element with no `content` at all,
  i.e. before the fix exists), then restored the fix and confirmed all 3
  pass again.
- Reviewer independently inspected (not just tested) the RTL
  gradient-direction override, the `Math.abs()` scroll-origin handling,
  `pointer-events: none` for hit-testing safety, and the
  `flex: none`/`order`/`position: sticky` interaction with the existing
  ut-docs#2000 focus-ring fix in the same media-query block — no issues
  found in any of them.

## Verified beyond automated tests

- Driven live (a throwaway `e2e/run-till.sh`-style server) and
  screenshotted: 360×740 in `en` (LTR) and `fa` (RTL), both at the true
  scroll start and scrolled to the last tab; 1024×600 kiosk floor in `en`
  and `tr` (unaffected, confirmed no fade renders and the strip still
  wraps); keyboard/roving-tabindex arrow-key navigation (the scroll
  listener fires correctly off the browser's own focus-driven auto-scroll,
  not just a manual `scrollLeft` set, and the existing focus ring is
  intact, not clipped).
- Pixel- and crop-level inspection of the RTL screenshots specifically
  (not just the automated assertions) confirmed the fade visually
  attenuates the cut-off glyph at the correct edge in both the at-rest and
  scrolled-to-end states — this is the exact case the rejected CSS-only
  approach got backwards, so it was checked by eye, not just by
  `getComputedStyle`.

## Gate (build/test/guards)

`go build ./...`, `go vet ./...`, `gofmt -l .` (empty — diff touches no
`.go` files), `go test ./...` (all packages green),
`golangci-lint run ./...` (0 issues), `guard-i18n.sh`,
`guard-data-access.sh`, `guard-help-topics.sh` — all green, run both
before and independently re-run during review. The neighbouring
`catalog-item-form-1956.spec.ts` and `catalog-item-form-2000.spec.ts`
suites (15 tests) were also re-run and remain green — no regression to
the feature this builds on top of.

**`guard-docs-shots.sh` initially missed, caught by real CI, fixed
same-cycle.** The local pre-push gate above didn't include it, and this
review's own first draft reasoned (wrongly) that no regeneration was
needed because the fade "only appears mid-scroll." That reasoning doesn't
match how the guard actually works: it hashes the *whole* `web/public/**`
file whenever it changes at all, regardless of whether a specific
screenshot's at-rest state is visually affected — `web/public/app.css`
changing is enough to go stale, full stop. CI's `build` job caught it
first-push. Ran `make docs-shots` for real (112 screenshots across 28
topics × 4 locales); only two — `web/help/img/en/sell.png` and
`web/help/img/en/till-designer.png` — came out with any pixel difference
at all, both unrelated screens (not the catalog item form), both looked
at directly and confirmed clean (no overlap, no broken layout — plausible
incidental re-render noise from a shared global stylesheet, not a
regression). Committed alongside the refreshed `manifest.json`;
`guard-docs-shots.sh` now passes locally and CI was re-triggered.
**Also caught and fixed in the same push**: the global git identity had
silently reverted to `Claude <noreply@anthropic.com>` between the initial
commit and this amend (the known ut-docs#1185 drift) — caught by the
per-commit identity re-check before amending, not by CI.

## Deferred / not in scope

- ut-docs#2032 — wire `tabBarFade()` to a `resize`/`orientationchange`
  listener (see finding 1 above).
- No `web/help/` manual-topic update: this is a decorative scroll
  affordance (a gradient hint on an already-scrollable, already-documented
  strip), not a new control or changed interaction — nothing a merchant
  needs instructed prose for. (`make docs-shots` regeneration was still
  required regardless — see above; that guard is triggered by surface
  file changes, not by manual-prose relevance.)

## Verdict

Safe to merge.
