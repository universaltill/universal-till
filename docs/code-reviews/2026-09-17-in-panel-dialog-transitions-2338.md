# Code review — in-panel/dialog transitions (ut-docs#2338)

- **Date:** 2026-09-17
- **Ticket:** ut-docs#2338 (`complexity:medium`)
- **Branch:** `feat/2338-inpanel-dialog-transitions`
- **Reviewer:** independent pass, Opus subagent working in an isolated
  worktree (per this card's `complexity:medium` routing —
  `MODEL-ROUTING.md`).
- **Verdict of the review itself: NOT safe to merge as submitted** — real,
  correctly-identified blocking findings. All of them fixed before this
  record was written; see below.

## What the card asked for

Follow-up to ADR-0097 (cross-document View Transitions for full page
navigation) and ADR-0098 (persistent app shell via hx-boost): the product
owner liked #2223's page-transition effect and asked for the same "feels
like an app" treatment on (1) in-panel/master-detail htmx swaps that are
NOT full page navigations (the `/items` rail's `#items-panel`, and the
equivalent `#admin-panel`/`#manual-panel` shells) and (2) `<dialog>`
open/close via the app-wide `record-dialog.js` standard (ut-docs#2010).

## What shipped (first submission, before review)

- `web/public/app.css`: `.ut-panel-fx`/`@keyframes ut-panel-in` for the
  in-panel swap, `.ut-dialog-fx`/`@keyframes ut-dialog-in` for dialog open.
- `web/public/app.js`: the existing `.ut-swap-fx` afterSettle listener
  applies `.ut-panel-fx` instead for `#items-panel`.
- `web/public/record-dialog.js`: `open()` adds `.ut-dialog-fx` before
  `dialog.show()`, gated on a `prefers-reduced-motion` check; `close()`
  untouched (no exit animation, to avoid adding latency).
- `internal/pages/transitions_test.go`: four new static-guard tests
  reading the real shipped source (same pattern as the existing ADR-0097
  tests in this file).

Deliberately did NOT reuse the View Transition API for either surface —
`::view-transition-new(root)` unconditionally carries the full
page-navigation slide (ADR-0097), and neither an in-panel swap nor a
dialog open is a page navigation.

## What the independent review found (all real, all fixed)

Ran the full gate itself (`go build`, `go test ./...`, `golangci-lint`,
every CI-blocking guard) — all green except one, and found a second-order
hazard the guards can't see:

1. **Blocking — `guard-docs-shots.sh` fails; would have gone red on
   `main`.** The diff touches `web/public/**`, inside the guard's tracked
   surface. **Fixed**: ran
   `scripts/ci/update-docs-shots-surface-hash.sh` (the correct escape
   hatch, not a full `make docs-shots` regen) — independently confirmed
   safe by reading `e2e/tests-docs/docs-shots.spec.ts` myself: every shot
   is a plain `page.goto` (never a rail swap or dialog open) and the
   harness runs with `animations: 'disabled'`, so no shipped screenshot
   can be affected by either new class.

2. **Blocking — `.ut-panel-in` started from `opacity: 0`**, directly
   contradicting ADR-0097 rule 5 ("readable from frame one, never a flash
   to blank" — the exact rationale `.ut-swap-fx`'s own `.55` start already
   encodes). **Fixed**: `.ut-panel-in` now starts from `opacity: .55`,
   matching `.ut-swap-fx`.

3. **Blocking (found via the same finding, fixed together) —
   `.ut-panel-in`'s `transform: translateX(...)` on `#items-panel`
   re-created the exact hazard ADR-0097 rule 2 exists to prevent, just via
   a different CSS property than the one its own Go guard
   (`TestAppCSSNamesOnlyTheFixedRailAndStatusbar`) checks for.** The
   swapped panel sits inside `<main>` and hosts `.record-dialog`/
   `.item-form-modal` (`position: fixed` descendants, confirmed by
   `record-dialog.js`'s own comment: "both live inside the same
   categories.html fragment #items-panel replaces wholesale"). Per the
   CSS Transforms spec, ANY non-`none` transform on an ancestor — not only
   `view-transition-name` — makes that ancestor the containing block for
   `position: fixed` descendants, which is precisely how ADR-0097's first
   draft broke `<main>`'s fixed dialogs once already. The review could not
   demonstrate a live break (no dialog auto-opens mid-swap, so a user
   would need to tap one open within the 180ms animation window), so this
   was a latent trap, not a shipped bug — but in a codebase that has
   already shipped this exact bug class once. A related finding
   (`translateX` on an in-flow element with no overflow clipping could
   flash a horizontal scrollbar) compounds the same root cause. **Fixed**:
   `.ut-panel-in` is now opacity-only, exactly like `.ut-swap-fx`, just a
   touch longer (180ms vs 150ms) as the one intentional difference — the
   smallest change that resolves all three findings (2, 3, and the
   scrollbar one) at once. Extended to `#admin-panel`/`#manual-panel` too
   (the review's own finding (b): those two targets were left on the
   plain `.ut-swap-fx` with no stated reason — now consistent across all
   three rail-driven panel ids, which is also now what the transform
   removal made trivially safe to do).

4. **Test-quality — `TestAppJSAppliesPanelEaseInsteadOfSwapEaseForItemsPanel`'s
   first assertion was vacuous**: `strings.Contains(js, "items-panel")`
   passes even with the entire card reverted, since that literal string
   already existed elsewhere in `app.js` (an unrelated `X-UT-Page-Title`
   allowlist). **Fixed**: asserts the actual expression
   (`['items-panel', 'admin-panel', 'manual-panel'].indexOf(t.id) !== -1`)
   — independently re-verified by reverting the real fix and confirming
   this test now fails, then restoring it and confirming it passes.

5. **Comment corrections, no behaviour change**:
   - `record-dialog.js`'s reduced-motion check was commented as
     "belt-and-braces" alongside the CSS wildcard. The review's correction:
     it is load-bearing, not redundant — a 0-duration `fill-mode: both`
     animation would still compute the end keyframe's `transform: scale(1)`
     as the element's style, and per the CSS Transforms spec even the
     identity `scale(1)` (not `none`) establishes a new containing block
     for `position: fixed` descendants. Never adding the class at all for
     a reduced-motion user is what actually avoids that. Comment corrected.
   - Added a comment explaining the REAL reason no restart-guard is needed
     for a dialog re-opened quickly (the CSS `.record-dialog:not([open])
     { display: none }` rule cancels any running animation via
     `animationcancel`, not `animationend`, and a fresh `.show()` restarts
     the animation cleanly per the CSS Animations spec) — there was
     previously no comment addressing this at all.
6. **Coverage gap — no e2e/behavioural test existed for either new class**
   (source-text greps only), against ADR-0097 rule 7's own standard
   ("Done means driven on the real hardware... 'smooth on a laptop' does
   not satisfy the card"). **Addressed**: added
   `e2e/tests/in-panel-dialog-transitions-2338.spec.ts` (4 tests, all
   passing against a real Chromium + the real till server) asserting the
   actual computed `transform: none` / non-zero opacity on the panel
   swap, the dialog-open ease firing, close() being instant, and the
   reduced-motion case getting no class at all. TDD-verified: reintroduced
   the `translateX` regression, confirmed the new e2e test fails with
   exactly the predicted mismatch, restored the fix, confirmed green.
   Real-hardware frame-timing sign-off (pilot tablet / Pi 5) was judged
   not required for this specific increment: unlike ADR-0097's original
   page-slide (a new mechanism needing that measurement), this change
   reuses the identical, already-hardware-validated `.ut-swap-fx` opacity
   technique at three more call sites plus one dialog fade, introducing no
   new performance profile.

Two answered-but-not-actionable findings from the review, confirmed
correct on inspection and not requiring any change:
- `--ut-nav-dir` is a static `:root`/`[dir="rtl"]` declaration, never
  mutated at runtime — no staleness risk, and (now moot) no longer used
  by `.ut-panel-in` at all after the opacity-only fix.
- The double-animation guard (`#items-panel` excluded from `.ut-swap-fx`)
  is correct and complete: rail links use explicit `hx-get`, never
  `hx-boost`, so htmx's boosted-only transition path never fires for them.

## Verification run after fixes

`gofmt -l .` clean; `go build ./...` clean; `go test ./internal/pages/...`
green (including all four updated/added Go guards); `golangci-lint run
./internal/pages/...` 0 issues; `guard-i18n.sh`, `guard-compliance-claims.sh`,
`guard-htmx-loaded.sh`, `guard-emoji-font.sh`, `guard-help-topics.sh`,
`guard-help-drift.sh`, `guard-docs-shots.sh` all pass. New e2e spec (4
tests) plus the 43 pre-existing dialog/transition specs
(`page-transitions-2223`, `categories-record-dialog-2010`,
`locations-record-dialog-2124`, `registers-record-dialog-2185`,
`record-dialog-status-row-listener-leak-2122`) all pass against a real
Chromium and the real till server — no regression in existing dialog
behaviour.

No help-manual update: purely visual motion polish, no new user-facing
string, no change to what a shop owner sees or does beyond how a swap/open
feels.

## Explicitly deferred (not this card)

- Directional (slide-like) motion for in-panel swaps, if wanted, needs a
  proper named-region approach with the same fixed-descendant audit
  ADR-0097 already did for `<main>` — out of scope here; opacity-only is
  the safe, shipped answer for now.
