# Code review: settings Payments card `.fee-row` e2e regression coverage

**Date:** 2026-08-21
**Scope:** `e2e/tests/settings-fee-row-251.spec.ts` (new file only, test-only change, no product code)
**Card:** universaltill/ut-docs#251

## What shipped

A new Playwright spec, `e2e/tests/settings-fee-row-251.spec.ts`, covering
the Settings page Payments card's `.fee-row` — the two real bugs the
independent review of ut-docs#81 caught live
(`docs/code-reviews/2026-08-01-settings-page-layout-fix.md`) but that
review deferred committing a regression test for:

1. **Input value clipping.** Percent/fixed number inputs must not clip a
   realistic max value (`100.00`% / `999.99` fixed) — asserted via
   `scrollWidth <= clientWidth + 1` (1px sub-pixel tolerance, same
   rationale `basket-no-horizontal-scroll-391.spec.ts` documents),
   checked both at the default viewport/scale and again at a narrow
   (390px) viewport with `--ui-scale` 2.
2. **Save button reachability below ~430px.** At a 390px viewport, the
   Save button must be reachable — either directly inside the card's box,
   or via `.fee-row`'s own `overflow-x: auto` scroll escape hatch —
   proved with a real, completing click (not just geometry), same
   standard `tender-panel-reachable.spec.ts` holds itself to.
3. **Bonus (per the issue's AC, not required):** the two
   `.settings-wide` cards (Backup table, All Settings table) render
   without their own internal horizontal scrollbar.

Follows the exact idioms of the three existing precedent specs it was
scoped against: `tender-panel-reachable.spec.ts` (button reachability via
a real click), `ui-scale-basket.spec.ts` (the ui-scale interaction
shape), and `basket-no-horizontal-scroll-391.spec.ts` (the
scrollWidth-vs-clientWidth clipping assertion, and its `afterEach`-based
ui_scale restore convention — used here in place of the UI-driven
select+submit restore, since that combination genuinely wasn't reliably
clickable at a 390px viewport during test authoring).

## Independent review (different-instance Sonnet, fresh context — complexity:easy)

**Verdict: safe to merge as-is.**

Two non-blocking nits, both accepted/addressed rather than deferred:

- The Save-reachability test's real click persists
  `payments.fee.<method>` (auth is off on the default e2e project, so
  `checkOrElevate` resolves to `allowed`, not gated). Confirmed genuinely
  idempotent — the row's DOM values are never filled before this click,
  so it re-saves whatever is already persisted — and confirmed no other
  spec asserts on `payments.fee.*` settings or audit rows. Added a
  one-line comment explaining this explicitly rather than leaving it
  implicit.
- The test implicitly depends on seed order for which payment method
  `.fee-row` `.first()` picks (effectively "Cash"). Deterministic today
  and the test doesn't care which method it is, only that geometry/
  reachability hold — noted, not fixed.

Everything else checked and cleared: selector uniqueness (no clash with
the Payments card's other, sibling `payments-default` form), the
`ui_scale 2` sub-describe's `afterEach` restore is correctly scoped to
that block only (can't leak into sibling tests or other spec files), no
hardcoded sleeps/flakiness risk, convention fidelity (`watchConsole`,
`as HTMLInputElement`, `el.closest(...)!`), no real client name/secret,
the bonus `.settings-wide` test is meaningful (guards the `overflow-x:
auto` escape hatch actually staying in place, not vacuous), and this is
genuinely new coverage (`grep -rln "fee-row" e2e/tests/*.ts` found only
this file plus a comment in `basket-no-horizontal-scroll-391.spec.ts`
naming #251 as a prior related card, not a competing test).

## Verification (real, beyond automated pass/fail)

- `go build ./...`, `go vet ./...` — clean (no Go changes in this diff).
- `bash scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all pass (no product code touched).
- `go test ./...` — full suite green.
- **TDD proof, done twice (once by the implementer, once independently by
  the reviewer):** `web/public/app.css`'s `.fee-row` rule was temporarily
  reverted to the pre-fix version (`3.6rem` input tracks, no
  `overflow-x: auto`, no reduced input padding) and the new spec re-run
  against it:
  - `percent/fixed inputs do not clip a realistic max value` → failed:
    `percent input "100.00" must not clip (scrollWidth 92 vs clientWidth 62)`.
  - `Save button is reachable at a narrow (390px) viewport` → failed:
    `Save must be reachable inside its card, directly or via .fee-row scroll`.
  - `at ui_scale 2 › ... do not clip at ui_scale 2` → failed:
    `scrollWidth 163 vs clientWidth 120`.
  - The unrelated bonus `.settings-wide` test correctly still passed.
  - `app.css` restored (`git checkout -- web/public/app.css`); re-run —
    all 4 tests passed again. `git diff web/public/app.css` confirmed
    empty (byte-for-byte restored) both times.
- Full existing e2e default-project suite: **141 passed, 1 failed** —
  `catalog-image-to-till.spec.ts` (image-load timing), the same
  pre-existing, unrelated flake documented in the 2026-08-01 review this
  card follows up on. No other failures; nothing newly broken.
- No real client/shop name or secret-shaped literal introduced.

## Deferred

Nothing further deferred — this card's acceptance criteria (including
the explicitly-optional bonus) are fully covered by the new spec.

## Verdict

Safe to merge. New coverage only, no product-code risk; independently
reviewed and TDD-proven against the real pre-fix regression.
