# Code review — basket qty/discount touch-target: documented decision, not a sizing fix

- **Date:** 2026-09-12
- **Ticket:** ut-docs#1340 (follow-up from independent review of ut-docs#1314 /
  universal-till PR #667)
- **Branch:** `fix/1340-basket-touch-target-decision`
- **Reviewer:** independent pass, different model (Opus) from the implementer
  (Sonnet), read-only review of the working tree (no shared-checkout mutation
  risk — the diff is comment-only, nothing for a revert-then-restore TDD
  check to apply to).
- **Verdict: SAFE TO MERGE**, after one fix round addressing the findings
  below. No blocker-class (money/tax/data-loss/security) issue was found, so
  per this pipeline's model-routing rules this is the only review round.

## What shipped

Comment-only change in `web/public/app.css`, zero selector/rule/markup
changes, zero `.go`/`.html`/`.ts` files touched:

- Fixed two **pre-existing** factual errors in the same file: `.btn.compact`
  (line ~1019) and the order-type switch (line ~1988) both cited "WCAG 2.5.5"
  for the 24×24px AA minimum — that's SC 2.5.8 (Target Size Minimum);
  2.5.5 is the *different*, 44×44px AAA "Enhanced" criterion. Corrected both
  to 2.5.8, since this card's own subject is exactly this citation.
- Extended the `.qty-input`/`.disc-input` comment (the one this card is
  about) with the closed decision: current sizes (~27.8-29.1px non-kiosk,
  ~37.5px kiosk) stay as-is. Filed `ut-docs#2217` as the real follow-up
  (dedicated +/- steppers or an asymmetric cell-padding hit-area — research
  and pick one there, not assumed here).

No code, markup, or test files change. `go build ./...` passes; nothing
Go-related was touched so `go test`/lint were not expected to move and
weren't re-run in full (see "Why no test run" below).

## Why a decision, not a sizing fix

The card's own acceptance criteria explicitly allowed this path: "Decide
... whether the 44px baseline should apply here regardless of the
`body.kiosk` class, or whether a smaller inline-edit control is acceptable
given it's a secondary action." The investigated options and why each was
rejected (full reasoning is now in the `app.css` comment itself, so it stays
next to the code it explains, not only here):

1. **Raise the visible box to 44px.** Already known impossible without
   re-breaking `sale-screen-213.spec.ts`'s ">=4 rows visible" AC — the
   `.line-inputs` stacking exists specifically because a prior attempt at
   full-size inputs overflowed the row budget (ut-docs#1314's own history,
   pre-existing comment).
2. **Widen the gap between the two inputs instead of the inputs
   themselves.** Same vertical budget, same failure: two inputs at 44px
   plus *any* gap between them need ~92-98px total, and rows are currently
   77-79px with only a measured 12.9px margin (ut-docs#1314 review's own
   number). No symmetric change fits.
3. **A symmetric invisible enlarged hit-area** (an out-of-flow `<label>`
   overlay per input, so it doesn't add row height). Rejected: with only
   ~4.25-5px of gap between the stacked inputs (`.25rem`, tracks
   `--ui-scale`/`--fluid-fs`), reaching 44px on each overlay overlaps the
   neighbour's by ~11-12px — a real risk of tapping discount when qty was
   intended, or vice versa, right at the shared boundary. Worse than the
   current distinct-but-undersized targets.
4. **An asymmetric overlay** (qty's hit-area growing into the row's own
   `.55rem` cell padding upward, disc's downward — never into the shared
   gap) reaches ~39px each with zero row-height cost. Genuinely closer, and
   not ruled out — but deliberately not built in this card: it's a real
   interaction change (hit-testing inside a `<td>`'s padding under
   `vertical-align: middle`, plus a third stacked child,
   `.line-order-type-phone`, that joins the same column below 480px and
   would need its own accommodation). ut-docs#2217 already owns redesigning
   this control (steppers or this overlay) — one coordinated redesign, not
   this card building half of one first.

Compliance framing: current sizes already clear WCAG 2.2's actual mandatory
floor (SC 2.5.8, AA, 24×24px). 44px is this product's own internal,
AAA-aligned aspiration, not a legal/compliance requirement — and at the
supported `ui_scale 2` setting these rem-sized inputs are already ~56px,
over 44, so the gap is specific to the default scale.

No new on-device verification is owed by this card: nothing here changes a
measured dimension, so ut-docs#1021's and #1314's existing on-device
numbers stand unchanged. A real-device check would only be new work if a
size or layout actually moved.

## Independent review — first pass findings and how each was resolved

The first draft of this decision (comment-only, same file) was reviewed by
a fresh-context Opus subagent before this record was written. Findings and
resolution:

1. **A safe partial fix (the asymmetric overlay above) wasn't mentioned at
   all** — the first draft's reasoning only ruled out symmetric expansion
   and read as "44 is impossible, therefore nothing is possible." Fixed:
   the option is now named explicitly, with the actual reason it's deferred
   to ut-docs#2217 rather than silently omitted.
2. **The `.25rem` gap was asserted as a flat "4px"** — wrong; it's
   `clamp(17px,...,20px)`-based (`--fluid-fs`) and scales with
   `--ui-scale`, so ~4.25-5px at the default scale, not a fixed value.
   Fixed: cited the actual CSS variable and range instead of a flat number.
3. **`ui_scale 2` mitigation omitted** — at that supported setting the
   inputs are already over 44px, which materially supports "acceptable at
   default scale" and was missing from the first draft. Added.
4. **Led with a weaker argument** — the first draft opened with "can't be
   done by padding alone," which doesn't rule out widening the gap instead.
   Fixed: now leads with the vertical-budget arithmetic (44+44+gap vs.
   77-79px rows, 12.9px margin) that rules out every symmetric variant at
   once, gap-widening included.
5. **Self-contradictory WCAG citation** — the new text correctly cited
   2.5.8, but two *pre-existing* mentions elsewhere in the same file still
   said "2.5.5" for the same 24px floor, now visibly disagreeing inside one
   file. Fixed: corrected both pre-existing mentions (see "What shipped"),
   since this card's own subject is exactly this citation.
6. **No decision record in `ut-docs`** — the reasoning lived only as a CSS
   comment in `universal-till`. Per `ut-docs/CLAUDE.md`, the docs repo is
   the source of truth for decisions and every substantive change needs a
   review record. Fixed: this file, plus the closing narrative comment on
   `ut-docs#1340` itself carries the decision for anyone reading the source
   of truth without cloning this repo.
7. **AC bullet 3 (real-device check) left unaddressed** — fixed by stating
   explicitly, in the comment and above, that no new device check is owed
   because no measured dimension changed.
8. **Follow-up issue (`ut-docs#2217`) inaccuracies** — its first draft
   claimed raising to 44px "was already tried and reverted" (false:
   ut-docs#1314 went 33-35px → 27.8-29.1px, a shave, never a 44px attempt
   that got reverted) and an uncited "mirrors competitor POS UX" claim, and
   scoped the fix to non-kiosk only even though kiosk's own 37.5px floor is
   also under 44px. All three fixed by editing the issue body: corrected
   the history claim, reframed the competitor-research claim as a TODO for
   that card (not asserted as already done), and widened scope to both
   kiosk and non-kiosk.
9. **A third stacked child** (`.line-order-type-phone`, below 480px) that
   the first draft's geometry reasoning didn't account for. Added to both
   the code comment and ut-docs#2217's scope so it isn't rediscovered from
   scratch there.

No second review round was run after this fix pass — none of the findings
above were blocker-class (money/tax/data-loss/security); per
`MODEL-ROUTING.md`'s process-depth rule, a second round has to be earned by
a blocker-class finding in the first, and none appeared here.

## Why no test run

The diff touches only prose inside CSS comments — zero selectors, zero
rules, zero markup, zero script. `sale-screen-213.spec.ts` and
`basket-no-horizontal-scroll-391.spec.ts` (the two specs the original card
asked to re-verify *if raising to 44px*) measure rendered geometry that
this change cannot affect, since no rule changed. `go build ./...` was run
twice (before and after the fix pass) and passes; `gofmt -l .` reports
nothing. Running the full Playwright suite for a comment-only diff would
cost real CI/wall-clock time for zero possible signal.
