# /help manual "panels gone" field report — investigation + e2e coverage gap (ut-docs#389)

## What was reported

A field report (P1, real device): "the help page style is completely gone
and shows everything in one page and the panels are in vertical mode" — the
`/help` manual's two-pane shell (nav tree `.manual-nav` + reading panel
`.manual-panel`, a CSS grid in `web/public/app.css`) appeared to render
unstyled/stacked. `ut-docs#361` (a duplicate-`routes:` entry silently
disabling the whole help-topic registry) was the leading suspect, but was
already fixed/closed before this report, and the reporter's own follow-up
comment argued against it: a failed registry would degrade *navigation*,
not strip page *styling*.

## Investigation (could not reproduce a live regression on `main`)

Built and ran the app from a clean checkout (`e2e/run-till.sh`, fresh temp
data dir — no on-disk `web/` override, embedded assets only) and drove
`/help` with a real headless Chromium (Playwright), not just `curl`:

- Every request (`/help`, `/public/app.css`, `/themes/monarch.css`, vendor
  JS) returned 200, no console/page errors.
- `#manual`'s computed style was `display: grid` with two real columns at a
  1280×800 desktop viewport.
- Re-checked at 900px, 390px (mobile) and in `fa` (RTL) — all rendered
  correctly: full styling preserved, correct column count for the
  viewport, correct RTL mirroring (nav right of panel).
- Confirmed pre-#182 `app.css` (`git show 591494f~1:web/public/app.css`)
  has **zero** `.manual` rules — a real "old CSS, new HTML" mismatch would
  produce exactly the reported symptom, but nothing in the current `main`
  build produces that state.

No code-level regression found. Two mechanisms were considered and are
plausible but unconfirmed without reporter/device detail (recorded in the
issue comment, not guessed into a fix here):

1. **Static-asset staleness**: `internal/pages/static_page.go`'s
   `newPublicFallbackFS` deliberately prefers an on-disk `web/public/`
   override over the binary's embedded copy (so a shop's custom theme
   survives an update) — while HTML templates are always served from the
   embedded FS only. If a till's on-disk `web/public/app.css` ever falls
   out of sync with its binary (a partial/manual deploy, or
   `internal/selfupdate`'s best-effort `web/` swap not landing), the new
   HTML markup would be served against old CSS. Not reproduced here — no
   access to the reporting till.
2. **Working-as-designed responsive collapse**: `app.css:934`
   (`@media (max-width: 52rem) { .manual { grid-template-columns: 1fr; } }`)
   intentionally stacks the two panes below 832px — explicitly for "a
   1024×600 Pi in portrait" per the comment beside it. A portrait kiosk, a
   zoomed browser, or an increased browser default font size (which *does*
   scale the `rem` breakpoint, unlike this app's own `--ui-scale`) could
   plausibly present as "panels gone / vertical" **without any bug** —
   though that alone doesn't explain "style is completely gone", since a
   stacked-but-styled layout (confirmed by direct screenshot at 390px) is
   fully colored/fonted, just single-column.

Flagged by the independent reviewer (see below) as the sharper of the two —
worth checking against the reporter's actual device before assuming (1).

## What shipped

Whatever the field cause turns out to be, the existing e2e coverage had a
real, independent gap: `manual.spec.ts`'s only layout assertion was
`.manual-nav`'s `toBeVisible()`, which only checks display/visibility — an
unstyled, stacked-but-present div still passes it. It would **not** have
caught this class of regression at all. Added `e2e/tests/manual.spec.ts`
test `manual renders as a real two-pane grid, not stacked divs`:

- Pins an explicit 1280×800 viewport (was implicitly relying on
  Playwright's default — see review finding below).
- Asserts `#manual`'s computed `display` is `grid` (true at every
  viewport width — the direct, width-independent probe that CSS actually
  loaded and applied).
- Asserts `.manual-nav` and `.manual-panel` sit side by side (not
  stacked) at the pinned desktop width, via `expect.poll` on
  `boundingBox()` with a descriptive failure message.

**Verified red/green twice** (before and after the review's requested
changes): temporarily serving the pre-#182 `app.css` (zero `.manual`
rules) makes the new test fail with exactly the reported symptom
(`toHaveCSS` reports `display: "block"`, not `"grid"`); the real `app.css`
passes. Neither `app.css` nor `playwright.config.ts` (both edited locally
only to drive this verification against the sandbox's pinned Chromium
build) are part of the committed diff — confirmed clean via `git status`
after each round.

## Independent review (fresh subagent, `complexity:medium` → Opus)

**First round: REQUEST CHANGES**, all addressed:

- **Should-fix — unpinned viewport**: the original draft relied on
  Playwright's unstated default (1280×720) to stay above the 832px
  responsive breakpoint; a narrower default elsewhere would fail on
  *correct* behaviour. Fixed: `page.setViewportSize({ width: 1280, height:
  800 })` pinned explicitly, matching this repo's own convention
  (`sale-screen-213.spec.ts`, `tender-panel-reachable.spec.ts`,
  `form-label-layout-300.spec.ts` all pin viewport when geometry is
  load-bearing).
- **Should-fix — comment described logical/inline geometry, code was
  physical (LTR-only) `x`/`width`**: reworded to state the LTR assumption
  explicitly and cross-reference the RTL test that covers the mirrored
  case.
- **Should-fix — geometry-only check was a weaker, less direct probe than
  the comment claimed**: added `toHaveCSS('display', 'grid')` on `#manual`
  as the primary, viewport-independent proof CSS applied at all name-checks
  the exact rule; kept the geometry check on top as the "and they're
  actually side by side" assertion under the pinned width.
- **Nits taken**: descriptive assertion messages on both new assertions
  (matching this file's own `settings sections carry their own ? hints`
  precedent); switched the bounding-box read to `expect.poll` (matching
  `ui-scale-basket.spec.ts`'s precedent for a non-instant layout read)
  instead of a single unretried `boundingBox()` call.
- **Nits not taken** (reviewer flagged, judged not worth it): a
  post-htmx-swap re-check of the grid (the pre-swap check plus the
  existing post-swap `.manual-nav` visibility check already cover the
  ticket's literal AC; the shell doesn't re-render on swap, so a second
  grid check is redundant, not defensive); the `+1` rounding tolerance
  comment being "inert not wrong" given `.manual`'s `1.2rem` gap — left as
  is, harmless.
- The reviewer's most valuable finding wasn't a diff nit: it named the
  responsive-collapse hypothesis above and argued it's a shorter path to
  closing #389 than another repro attempt — folded into the issue comment
  as the concrete question for the reporter (screen resolution,
  orientation, zoom, browser default font size).

Second pass (self, after applying the above): re-ran the full
`manual.spec.ts` suite (14 tests, all green) and repeated the red/green
`app.css` swap against the revised assertion — both confirmed above. Not
re-sent to the reviewer subagent per this pipeline's one-review-round
default; the changes were mechanical and directly responsive to the
findings, not new work.

## Why this issue is NOT being closed

Per the ticket's acceptance criteria: (2) is met (e2e coverage now fails if
either panel disappears or the grid doesn't apply); (3) is met (ruled out
#361 — already fixed, and mechanically doesn't match "unstyled"); (1) is
**not** met — no live code regression was found and reproduced on `main`,
so there's nothing to "fix the cause" of yet. Closing the ticket would
misrepresent an unconfirmed field report as resolved. `ut-docs#389` stays
open, labelled `needs-info`, with a comment asking the reporter for
device/viewport/zoom/font-size detail per the responsive-collapse
hypothesis above, and this PR references (not closes) it.

## Safe to merge

Yes — test-only change, `e2e/tests/manual.spec.ts` diff reviewed above, no
production code touched. Feature branch
`fix/389-help-manual-layout-regression-test`, merged via `merge` (not
squash/rebase, per this pipeline's standing merge-method rule).
