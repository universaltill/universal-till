# Code review: flaky sale-screen-213 ">=4 basket lines visible" test

**Card:** universaltill/ut-docs#320
**Date:** 2026-08-09
**Complexity:** medium — Dev inline (session model), Review via a fresh-context
Opus subagent, independent of the Dev's own reasoning.

## Background

`e2e/tests/sale-screen-213.spec.ts`'s "≥4 basket lines visible without
scrolling at 1280x800" test intermittently failed in CI with `Received: 3`
(expected `>=4`). Real evidence from the issue: across 4 identical-commit CI
runs, it failed twice and passed twice, both failures on the initial attempt
*and* its automatic retry — the signature of a real flake, not a one-off
deterministic regression.

## What shipped

**Root-caused, not guessed** — instrumented the real DOM across 100 basket
swaps (via the independent review; see below) rather than trusting the first
plausible-looking theory. `.line-name`'s `-webkit-line-clamp: 2` rows measure
identically (37.7px, already clamped) at every observable stage, including
synchronously inside `htmx:afterSwap` — never the moving part. What actually
settles a frame late is **`.basket-scroll`'s own resolved height**: it's
`flex: 1` inside the `.basket` flex column (`web/public/app.css`), and its
height lags a fresh row swap by exactly one frame (623.78px → 635.38px)
while the rows inside it are already final. The margin between the 4th
(last fully-visible) row's bottom edge and the box's own bottom edge is
razor-thin — ~11px once settled, briefly *negative* while the box is still
mid-resize — so a measurement taken before the box itself finishes resizing
intermittently undercounts.

Added `waitForStableLayout(page, selector)` to `e2e/tests/helpers.ts`: polls
`getBoundingClientRect()` of every element matching `selector` across
consecutive `requestAnimationFrame` ticks, returning once two consecutive
frames produce an identical snapshot. Throws (rather than silently
returning) if the selector matches nothing, or if nothing stabilizes within
`maxFrames` — a genuine layout regression must fail loudly and diagnosably,
not look identical to success. Called in the flaky test with
`'.basket-scroll, .basket-scroll tbody tr'` — the container that actually
moves, not just its already-stable children.

Also attached full diagnostics (box rect, per-row rects, root font-size) to
the assertion's failure message, so a future occurrence is self-diagnosing
instead of another bare `Received: 3`.

## Independent review (fresh-context Opus)

First pass caught that the Dev's initial fix was built on a **wrong
mechanism**: it attributed the flake to `-webkit-line-clamp`'s Chromium
implementation possibly needing a second layout pass, and the helper only
watched row rects. Since the rows never move, the stability check exited
after exactly one frame *every time*, by luck — the fix "worked" (100%
locally, ~85 repeat-each runs) but for the wrong reason and with no real
guarantee.

The reviewer drove Chromium directly against a real till (couldn't run the
spec harness itself — this sandbox's installed browser revision doesn't
match the pinned Playwright version) and instrumented the actual swap
synchronously at `htmx:afterSwap`, catching the unsettled state 17/20 times
on demand — a real, on-command repro the Dev's own repeat-each attempts had
missed because they were watching the wrong element.

**Findings, all fixed in this diff:**
- **F1 (blocking):** stated root cause wrong — real mechanism is
  `.basket-scroll`'s flex-height settle, not line-clamp. Comments in both
  `helpers.ts` and the spec rewritten to describe the verified mechanism.
- **F2 (blocking):** helper watched only rows (never move) → "stabilizes"
  after 1 frame regardless of whether the actual moving element (the box)
  had settled. Fixed by including `.basket-scroll` itself in the selector.
- **F3 (minor):** silent no-op if `selector` matches zero elements (e.g. a
  future rename). Fixed — throws.
- **F4 (minor):** silent bail-out after `maxFrames` looked identical to
  success. Fixed — throws with a diagnosable message.
- **F5 (informational, not fixed — accepted residual risk):** even fully
  settled, the AC's real margin is only ~2-3px per row (~3% of a row
  height), and `.line-name`'s own existing CSS comment already documents
  that CI/Linux font metrics have wrapped these names to an extra line
  before. This fix removes the settle-timing race; it cannot guarantee the
  margin itself is never exhausted by a font-metric difference. Diagnostics
  were added specifically so a future occurrence here is distinguishable
  from this one at a glance.
- **F6 (informational — filed separately, not touched here):** while ruling
  out a competing hypothesis, the reviewer found a real, unrelated
  test-isolation hazard — `basket-no-horizontal-scroll-391.spec.ts`'s
  `ui_scale matrix` tests reset `ui_scale` manually at the tail of the test
  body rather than in an `afterEach`, so a failed assertion mid-matrix
  leaks `ui_scale` into every later spec on the shared server. Confirmed a
  leak reduces `sale-screen-213`'s visible-row count to ≤2 (not 3), so it
  is **not** this bug's cause — but it's real and worth its own fix. Filed
  as `universaltill/ut-docs#510`.

## Verified beyond the automated suite

- Full `sale-screen-213.spec.ts` (all 7 tests) and
  `basket-no-horizontal-scroll-391.spec.ts` (all 16 tests, including the
  `ui_scale` matrix) run together, in the same alphabetical order CI uses —
  23/23 pass.
- The target test alone repeated 25 times (`--repeat-each=25`) post-fix, all
  pass — on top of the reviewer's own 17/20 on-demand repro of the
  *unfixed* race and confirmation the fixed version no longer exhibits it.
- Diff scoped to exactly the two files needed
  (`e2e/tests/helpers.ts`, `e2e/tests/sale-screen-213.spec.ts`) — confirmed
  via `git diff --stat` before every commit; a local-only Playwright
  `executablePath` override used to run tests in this sandboxed session
  (its installed browser revision doesn't match the pinned
  `@playwright/test` version) was reverted before each commit and is not
  part of the shipped diff.

## Honesty note

Could not personally reproduce the original failure locally in ~85
repeat-each runs pre- and post- the first (wrong-mechanism) fix attempt —
the sandboxed environment's font/rendering stack didn't hit the same
timing window the reviewer's synchronous `htmx:afterSwap` sampling did.
The reviewer's direct, on-demand repro (17/20) is the real evidence this
fix addresses the actual mechanism, not a coincidence of one accidental
frame wait.

## Safe-to-merge verdict

Yes, with the above residual risk (F5) explicitly accepted and
disclosed rather than hidden — this closes the settle-timing race that
caused the specific observed flake signature; it does not (and cannot, by
itself) guarantee the AC's underlying margin is never exhausted by a
font-metric difference on some future CI runner. If this test flakes again
post-merge, check the new diagnostic output first — it will show whether
the box/rows were settled (a new, different bug) or whether the margin was
simply consumed (F5, expected residual risk, would need a CSS-side fix,
e.g. more headroom in `.basket-scroll`'s budget).
