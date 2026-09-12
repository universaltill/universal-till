# Code review: held-sales strip scroll affordance (ut-docs#2128)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2128
**Branch:** `fix/2128-held-strip-scroll-affordance`
**Complexity:** medium (Dev: Sonnet inline, Review: Opus, isolated-worktree subagent)

## What shipped

A real product-owner report on the pilot TECLAST tablet (1280×800):
holding a sale leaves no visible way to find it again. Root cause:
`.tender-scroll` (`app.css` — the flex container wrapping the barcode
scan row + the `#held-sales` strip) already has `overflow-y: auto` and
genuinely scrolls once its content doesn't fit, but had no visible cue
that there was more to scroll to — many kiosk browsers hide native
scrollbars entirely. Same failure class as `.products`' own pre-#1313
bug.

Live measurement (headless Chromium against the real e2e server, not
assumption) found the bug is broader than the original device report
alone showed: the held chips are **not real hit-test targets the moment
there is even 1 held sale**, at both the pilot device's own resolution
(1280×800) and the existing 1024×600 kiosk floor — not only at higher
held counts, and not only at 1280×800.

Fix: port `.products`' existing scroll-shadow gradient recipe (two
gradient pairs, `background-attachment: local`/`scroll`, self-hiding at
whichever edge has no unseen content — ut-docs#1313) onto
`.tender-scroll`, sized down (16px/8px vs `.products`' 24px/10px) for its
much shorter box. Deliberately does **not** touch the
`@media (max-height: 710px)` grid-row-ratio override — that budget has
been independently re-measured four times already for the 1024×600/
850×700 cases the existing test suite pins, and reopening it risks
exactly the regression class its own comments warn about.
Scrolling-with-a-visible-affordance is the accepted, correct outcome per
this card's own acceptance criteria, confirmed with the product owner's
own AC wording rather than assumed.

Confirmed via real hit-testing (not bounding-rect-vs-window comparison,
which can't see clipping by a scrolled `overflow:auto` ancestor at
`scrollTop:0`) that the chip is reachable via `scrollIntoViewIfNeeded()`
both before and after this change — this is a discoverability/affordance
bug, never a `.tab-panel`-style collapse-to-zero-height regression.

## Tests

- `e2e/tests/held-strip-scroll-affordance-2128.spec.ts` (new): mirrors
  `products-scroll-affordance-1313.spec.ts`'s own structure — asserts the
  background-layer wiring (4 layers, `local/local/scroll/scroll`, 2
  `radial-gradient`s) and that programmatic scroll actually reaches the
  chip.
- `e2e/tests/tender-panel-reachable.spec.ts`: extends the existing
  held-sales-matrix test with real hit-testing of the held chips
  themselves (`scrollIntoViewIfNeeded` + `elementFromPoint`), at 1280×800
  and 1024×600, 1-3 held sales — the exact gap that let the original
  report through undetected (the existing suite only ever hit-tested the
  Payment/quick-pay footer below the strip).
- `e2e/tests/helpers.ts`: adds `clearAllHeldSales`, since held sales are
  persistent DB rows (not per-context state `/api/pos/reset` clears) and
  this suite shares one server/DB across every spec file.

## Independent review (Opus, isolated worktree, different model from the Sonnet implementation)

**Initial verdict: FAIL — one blocker, five non-blockers, all fixed in a
follow-up commit on this branch. Final verdict: safe to merge.**

Confirmed independently, not just re-read from the implementer's claims:

- **Re-ran the TDD red→green cycle personally**, in the isolated
  worktree: reverted just the new `app.css` rule to the pre-fix version,
  re-ran `held-strip-scroll-affordance-2128.spec.ts` — real, specific
  failure (`expected 4 background layers split local/local/scroll/scroll`,
  received `[]`), not a timeout. Restored, re-ran — green. Confirmed the
  preceding overflow-precondition assertion passed on the *reverted* CSS
  too, so the test proves the fix, not the precondition.
- Verified the RTL "direction-agnostic" claim **by measurement**, not
  assumption: computed `background-position`/`background-size`/
  `background-attachment` under `dir=rtl` (fa) is byte-identical to `en`
  — no `left`/`right` introduced.
- Verified live geometry: `.tender-scroll` is 70.2px tall at 1280×800
  with a genuine 25px overflow at 1-3 held sales (chips wrap onto one
  line) — the card's overflow precondition is real, not contrived.
- Confirmed the gradient background layers cannot cover any interactive
  element (they're the container's own *background*, paints behind every
  descendant) — corroborated by the pre-existing scan-row/quick-pay
  hit-test tests still passing.
- Ran the full Go gate (`gofmt`, `build`, `vet`, `test`, `golangci-lint`)
  — clean, as expected for a diff touching no Go.
- Ran 13 CI guards directly — confirmed 12 pass, 1 fails
  (`guard-docs-shots.sh`) — see Blocker below.
- Scanned the diff for a real client/shop name and for any secret-shaped
  literal — none found.

### Blocker found and fixed

**`guard-docs-shots.sh` failed** — the guard hashes `web/public/**` into
its manual-screenshot-freshness check (an `app.css` change is exactly as
visible in a screenshot as a template change), and the commit had edited
`app.css` without regenerating `web/help/img/manifest.json`. Proven by
computing the guard's surface hash with the CSS change present vs.
reverted — the parent-commit hash was restored exactly, confirming this
diff was the sole cause; every other recent `app.css`-touching commit in
this repo's history had regenerated the manifest, this one was the
outlier. **Fixed**: `make docs-shots` (124/124 screenshots pass),
committed the regenerated manifest + changed PNGs
(`en`/`ar` `sell.png` + `till-designer.png`, `fa`/`tr` `catalog.png` —
the latter two are the guard's own known whole-surface-hash side effect,
ut-docs#2102, unrelated to this change's actual content, confirmed by
byte-level diff size). Actually looked at the regenerated `sell.png`
shots (en) — sale screen renders correctly, no regression.

No manual-topic prose update needed (correctly identified by the
implementer and confirmed by review): this is a passive visual affordance
with no new control or behaviour a shop owner would be taught to use —
only the screenshot half of the "manual ships with the feature" rule
applies here, not the steps/prose half.

### Non-blockers found and fixed

1. **`clearAllHeldSales` raced `#held-sales`' own async htmx swap** — the
   strip is a bare placeholder at page load until its `hx-trigger="load"`
   GET completes; reading `.held-chip` before that swap lands could see 0
   and wrongly conclude there was nothing to clear. Demonstrated with an
   artificially delayed `/ui/held` response (not observed on an idle
   machine — latent, not a live failure at review time). **Fixed**: wait
   for `#held-sales.held-strip` to attach (not `visible` — the swapped-in
   fragment can be legitimately empty) before counting, at function entry
   and after each reload.
2. **New `tender-panel-reachable.spec.ts` test leaked held sales on
   assertion failure** — cleanup was the last statement of the test body
   with no `afterEach`/`try`-`finally`, so a thrown hit-test assertion
   (precisely the regression this test exists to catch) would skip
   cleanup and leak up to 3 held sales into every later spec sharing the
   suite's server/DB. **Fixed**: wrapped the hold-loop in `try`/`finally`.
3. **`clearAllHeldSales`'s loop-termination guard was incomplete** — only
   guarded malformed `hx-vals`, not a resume that silently fails to
   actually clear the row (`hold_api.go` deliberately swallows a failed
   `repo.Get`/`Delete`, "a stale row is the lesser evil"), which would
   spin until Playwright's own 30s test timeout with no diagnostic
   pointing at the stuck id. **Fixed**: capped at 20 iterations, throws
   naming the still-stuck chip's id if exceeded.
4. **Stale cross-reference in `.products`' own dark-theme-debt comment** —
   still said "only `.products` gets this for now", so a future
   dark-theme fixer following that comment's own trail would miss the
   second `rgba(255,255,255,0)` occurrence this change adds. **Fixed**:
   comment now names both sites.
5. **Doc-comment misplacement in `helpers.ts`** — the new function had
   landed between `deactivateAllTables`'s own doc comment and its body,
   orphaning that comment and reading as `clearAllHeldSales`'s own
   documentation; its text also said "same shape as `deactivateAllTables`
   **above**" while sitting **above** it. **Fixed**: moved
   `clearAllHeldSales` (with its own comment) below `deactivateAllTables`.

### Noted, not a defect

The new spec asserts computed style (background-attachment/gradient
count) rather than a pixel-level screenshot diff, so it would stay green
if an opaque child later covered the shadow band. Confirmed by hand
(element screenshots at 1280×800, 1 and 3 held sales) that the cue does
render as intended today. This is the same limitation the #1313 precedent
already has — consistent, not a regression, flagged only so it isn't
over-trusted later.

## Verification beyond automated tests

- Full `npx playwright test --project=default` (441/441) run once after
  the initial Dev/Tester pass, confirming no regression anywhere in the
  sale-screen/tender-panel suite; the three directly-relevant specs
  (`held-strip-scroll-affordance-2128`, `tender-panel-reachable`,
  `products-scroll-affordance-1313`) re-run green again after the
  review-fix commit.
- Screenshots taken and actually looked at (not just asserted) at
  1280×800 and 1024×600, at 0 and 3 held sales, before and after
  scrolling `.tender-scroll` to bottom — confirmed the strip is genuinely
  invisible at rest with 3 held sales (matches the reported bug exactly),
  the chips render correctly and are real hit-test targets once scrolled,
  and a close-up crop confirms the gradient shadow is present (subtle,
  matching `.products`' own established look) at the unscrolled bottom
  edge.
- Not verified: real touchscreen hardware (headless Chromium only — this
  fix is a passive visual affordance with no new gesture/pointer handler
  or touch target, so lower-risk than usual, stated explicitly rather than
  left implicit). Dark theme: not applicable, no dark theme plugin exists
  yet (same deferral `.products`' own comment already documents).

## Safe-to-merge verdict

**Yes**, after the blocker + five non-blocker fixes above, all verified
in the same branch: full Go gate clean, all 13 relevant CI guards green
(including `guard-docs-shots.sh` after regeneration), full e2e suite
green, TDD claim independently re-verified via real revert-then-restore
both before and after the review-fix commit.

## Explicitly deferred (not this card's scope)

- Whether `#held-sales` belongs inside `.tender-scroll` at all, given the
  strip needs a scroll-shadow cue at every held count ≥1 rather than only
  at high counts — flagged in the Architect design as a real, larger
  structural question deserving its own live-measurement rigor. Not
  decided here; a Backlog follow-up should be filed if a future card
  wants to pursue it.
- Dark-theme `color-mix()` swap for the now-two `rgba(255,255,255,0)`
  sites (`.products`, `.tender-scroll`) — no dark theme plugin exists yet
  to verify against; both sites' comments now point at each other so a
  future fixer catches both in one pass.
