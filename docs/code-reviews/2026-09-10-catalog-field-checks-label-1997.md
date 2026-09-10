# 2026-09-10 — `.field-checks` checkboxes stay beside their text (ut-docs#1997)

## What shipped

`.catalog-form label:not(.catalog-detail label)` (specificity `0,2,2`) beat
`.field-checks label`'s row layout (`0,1,1`) for **both** shapes
`.field-checks` is used across this codebase — a `<div class="field-checks">`
wrapping several plain `<label>` children (isWeighed/stockUntracked/isActive
on the catalog item form's Details tab), and a class applied directly to a
single standalone `<label class="field-checks">` ("Plain code" on Details,
"Set as primary" on the Keypad tab). The bug report named only the two most
visually dramatic instances (the standalone-label shape, stretched to the
form's full width); the div-wrapped shape was equally broken (box above
text, just narrower and less visually obvious) — confirmed live before the
fix: `label:has(#item-active)`'s computed `flex-direction` was `column`.

Fix, in `web/public/app.css`:
- The blanket column rule (and its `> input`/`> select` sibling) now also
  excludes `:not(.field-checks):not(.field-checks label)`.
- A new `label.field-checks` rule gives the standalone-label shape the same
  row layout `.field-checks label` already gave the div-wrapped shape — kept
  as two separate rules (not one selector list) only for `margin`: a
  descendant label sits inside `.field-checks`'s own `.3rem 0 .7rem` margin,
  so `margin: 0` there is correct; a standalone `label.field-checks` has no
  such wrapper and needs its own `margin: 0 0 .55rem` to match every other
  field's spacing.

New e2e spec `e2e/tests/catalog-field-checks-label-1997.spec.ts` pins the
geometry (checkbox top within the label's own line box, `<32px` tall,
`flexDirection !== 'column'`) for both shapes on the Details and Keypad
tabs, at 1024×600 and 360px — the same shape as ut-docs#1956's own F4 test,
not just the computed `flex-direction`. A fourth check confirms an ordinary
(non-`.field-checks`) label (`label:has(#item-name)`) still correctly stays
column-flexed at both viewports, i.e. the new `:not()` exclusions didn't
widen past the two `.field-checks` shapes.

## What the independent review found

Fresh-context Sonnet subagent (complexity:easy → Sonnet review per
`MODEL-ROUTING.md`), isolated worktree. **Verdict: PASS, no blockers.**

- Verified the specificity math by hand (not by trusting the code comment):
  `.catalog-form label:not(.catalog-detail label)` = `(0,2,2)`, confirmed
  correct; both new `:not()` exclusions confirmed non-redundant — removing
  either one re-admits exactly one of the two `.field-checks` shapes into
  the blanket column rule (verified via a live matched-CSS-rules dump per
  element).
- **Real finding, not a blocker — the fix's blast radius is wider than the
  bug report named, correctly:** `.field-checks label`/`label.field-checks`
  are global rules, not scoped to `.catalog-form`. This also fixes the
  identical latent bug on `tax_codes.html` (whose `.field-checks` div sits
  inside a `.card.catalog-form` wrapper — same collision) and
  `receipt_designer.html` (whose `.field-checks label` rule had **no**
  `display: flex` at all before this diff, relying on default inline flow
  that happened to look right). Confirmed both pages render correctly
  post-fix with a fresh server build. Called out here per the review's
  request, since it wasn't in the original task description.
- Nit (non-blocking): the test's `sameRow` geometry check is, on its own, a
  weak discriminator against a badly-broken layout (the reviewer showed a
  tall stacked box can still satisfy it) — not a problem in practice,
  because `expect(m.dir).not.toBe('column')` and `expect(m.h).toBeLessThan(32)`
  both fire first and are the actually load-bearing assertions (confirmed
  by the TDD revert below: all three fail on the `dir` check pre-fix).
- Confirmed no hidden-input siblings inside the div-wrapped `.field-checks`
  (the `isActive=0` fallback) participate as flex items — UA default
  `display: none` stays unoverridden.
- Confirmed no RTL-sensitive directional properties introduced
  (`flex-direction: row` is logical by nature; the new margin is symmetric).
  No new user-facing strings. No client/shop names or secret-shaped
  literals in the new test.

## TDD re-verification

Independently, by the reviewer: reverted `web/public/app.css` to
`origin/main`, re-ran the new spec — **all 3 tests genuinely failed**, each
on `expect(m.dir).not.toBe('column')`. Restored the fix, re-ran — **all 3
passed**. Confirms the tests actually exercise the real defect, not a
false-pass.

## Verified beyond automated tests

- `go build ./...` clean.
- `npx playwright test catalog-field-checks-label-1997 catalog-item-form-1956`
  — 15/15 passed (run twice for confidence after one unrelated environmental
  flake — see below).
- Manual driven-run screenshots at 1024×600 confirming the Details tab
  ("Plain code", Weighed/Stock-untracked/Active) and Keypad tab ("Set as
  primary") all render checkbox-beside-text, one line tall.
- `scripts/ci/guard-i18n.sh` and `scripts/ci/guard-e2e-fixtures-import.sh`
  both clean.
- One environmental flake noted by the reviewer (an orphaned `.ut-e2e-bin`
  process left over from Playwright's `reuseExistingServer` serving stale
  embedded CSS to a later, unrelated ad hoc check) — not a defect in this
  diff; resolved by killing the stale process and re-running against a
  guaranteed-fresh build. CI always builds fresh, so this doesn't recur
  there.

## Safe to merge

Yes — independent review PASS, no blockers, TDD claim re-verified for real.

## Explicitly deferred

- No pixel-level screenshot diffing was run (geometry assertions +
  manual look only, per `ux`'s "screenshot exists and was looked at" gate).
- CI-only guards not directly touched by this change (`golangci-lint`,
  `go test ./...`, other `scripts/ci/guard-*.sh`) were run once at the
  Dev/Tester stage, not re-run by the reviewer — none are CSS/e2e-spec
  sensitive for this change.
