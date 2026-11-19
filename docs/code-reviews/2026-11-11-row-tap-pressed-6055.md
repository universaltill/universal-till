# Review: pressed state on row-tap lists (ut-docs#6055)

Date: 2026-11-11 · Branch: `fix/6055-row-tap-pressed`
Author: Sonnet (Dev) · Tester: Sonnet · Reviewer: Opus 5.5, independent fresh context

## What shipped

- `web/ui/layouts/base.html`: the ut-docs#3000 press-feedback script's
  `pressTarget(t)` also returns the closest `[data-record-open]` row, so
  capture-phase `pointerdown` marks it `.ut-pressed` synchronously and the
  existing release / cancel / 10 px slop / 1 s backstop clear it. A row is
  marked exactly when `record-dialog.js`'s click handler would open it: the
  target is in the row's `[data-record-edit]` control, or in no
  `button, a, input, select, textarea, label, form` (the same selector).
  Never inside a `dialog`. Anything else falls through to the unchanged
  nav-link/`.nav-toggle` checks.
- `web/public/app.css`: `[data-record-open].ut-pressed > td { background:
  var(--border) }` after the `:hover` rule (equal specificity, so it wins;
  `--border` is a step darker than `--surface-2` in every theme, so it shows
  over a sticky hover). On the ≤480px `table.t-cards` card list, hover and
  pressed paint the row (the rounded card) and leave its cells transparent,
  except a dragged category row (`.is-dragging`), which keeps its own cell
  colour. Colour only, so it is the same under Light effects and reduced
  motion (ADR-0119).
- `e2e/tests/row-tap-pressed-6055.spec.ts` (new, 6 tests): touch down
  presses the row on /categories and /locations, a pan past the slop or a
  release clears it, a tap still opens the dialog; a link inside a row and a
  row inside an open dialog are not pressed (the row's edit button is, as a
  control); on a 360 px phone the pressed colour paints the card, not its cells.
- `web/help/img/manifest.json`: docs-shots surface hash only. No
  screenshot pixel changes: the new states exist only during a press or
  hover, which the harness never produces.

## TDD

Re-verified by the reviewer in one revert → run → restore sequence:
- `base.html` + `app.css` at `origin/main`: 4 of 6 fail. Both "touch down
  paints the row pressed" tests fail with `row marked pressed on touch
  down`, the link/dialog test fails on its control assertion, and the card
  test fails on `the card is painted pressed`. The two "tap still opens the
  dialog" tests pass because they only check for regressions.
- Dialog guard removed (`!row.closest('dialog')`): the link/dialog test
  fails with `a row inside a dialog`. The guard is now really exercised.
- Dev's `app.css` (no card-mode rule): the card test fails.
- Restored: 6/6 pass.

## Findings

| # | Sev | Where | Status |
|---|---|---|---|
| R1 | Low | `base.html` `pressTarget`: a press on a link inside a row (kitchen stations' "View display") returned `null` before reaching the nav-link path. That changed what earlier code did to such a link (it was marked `.ut-pressed`, though no style paints a plain link). The control list also differed from `record-dialog.js` (`a[href]`/`summary` vs `a`/`form`), so the row could show pressed for a press that never opens it, or the reverse | Fixed: the row check now mirrors `record-dialog.js`'s open condition and otherwise falls through to the unchanged link checks |
| R2 | Low | `app.css`: `[data-record-open]:active > td` painted the row for a press on any control inside it, including the link the JS deliberately excludes. `:active` also propagates to ancestors | Fixed: `:active` dropped. The JS marker covers mouse and touch, and keyboard users have the focus ring |
| R3 | Low | Tester (a): "a press inside an open dialog marks no row" passed without the fix because its target was an input in no row | Fixed: the test now injects a `[data-record-open]` row into the open dialog and checks it is not pressed, with a positive control and a link-in-row case. Removing the guard now fails it (see TDD) |
| R4 | Low | Tester (b): on the ≤480px card list the pressed colour was a square block of cells inset in the rounded card. Hover looked the same way | Fixed: in card mode hover and pressed paint the `tr` (the card) and the cells stay transparent. `.is-dragging` was excluded because the first draft would have overridden the dragged category's cell colour (`app.css` ~6829). A new e2e test pins it |
| R5 | Info | Stickiness: an htmx swap that replaces the pressed row leaves the class only on the detached node, and the next `pointerdown` or the 1 s backstop clears `pressEl`. A dialog opens on `click`, after `pointerup` has already scheduled the clear | No change |
| R6 | Info | Long-press drag on categories shows pressed briefly until the drag's movement passes the slop (Tester) | Accepted. It is harmless and matches the press |

## Verification beyond the spec

- `go build ./...`, `go vet ./...`: clean.
- e2e (Chromium, own till on private ports): new spec, `tap-feedback-3000`,
  `category-list-drag-2699`, all ten `*-record-dialog-*` specs: **109 passed**.
- Guards: i18n, rtl-logical-css, no-showmodal, fixed-dates, help-topics,
  wip-commits, compliance-claims, competitor-naming, kiosk-engine: pass.
  docs-shots: passes after `update-docs-shots-surface-hash.sh`. The commit
  needs the `Docs-Shots-Unchanged: true` trailer (no rendered pixel changes
  at rest).
- Tester's run (CDP-emulated touch, not real hardware): /categories at
  1280×800 in light, dark and amber, 360 px card layout in light and dark,
  and fa RTL at 1280 and 360. Measured colours: light rest transparent,
  hover `#f8fafc`, pressed `#e2e8f0`; dark hover `#1e293b`, pressed
  `#334155`; amber `#f6efe6` / `#ece2d5`. Text stays readable and RTL is
  fine. Shots are in the session scratchpad (`shots/*-pressed.png`,
  `*-hover.png`). The Tester ran 109 specs with the Dev version.
- The reviewer took a 360 px screenshot after R4
  (`shots/review-6055-card-light.png`): the pressed card is painted
  edge to edge inside its rounded border.
- Not covered: real touchscreen / iOS WebKit, the monarch / fresh / slate /
  monochrome themes, and the kitchen stations, promotions, country settings
  and fiscal register lists (no rows on the seeded till; they use the same
  attribute and rule).

Pre-flight: help topic n/a (the manual already says "tap a row"; a press
tint adds nothing to the steps) · docs-shots updated (surface hash only,
no pixel change) · reference page n/a (`list-and-dialog-pattern.md`
does not describe row hover/press feedback) · README n/a · no en.json keys.

## Verdict

Approve with the R1–R4 fixes above, which are in the working tree on top
of the WIP commit. Squash the WIP commit with a real subject and the
`Docs-Shots-Unchanged: true` trailer before the PR.

## Deferred

None. Real-device touch confirmation is optional and can wait for the next
tablet session.
