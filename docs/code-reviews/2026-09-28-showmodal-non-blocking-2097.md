# Review — till dialogs stop using showModal() (ut-docs#2097)

Date: 2026-09-28 · Branch: `fix/2097-showmodal-non-blocking` · Lane: cloud-54
Author: Opus 5.5 (Dev subagent) · Reviewer: Fable (independent, fresh context)

## What shipped
The product owner decided that a dialog which leaves the status bar, Lock or
exit-to-OS unclickable counts as blocked, with no exception for small
dialogs. Five till dialogs opened with `showModal()` and made the whole page
inert, so they now open with `.show()`: `#modifier-modal` (3 openers),
`#order-type-prompt-modal`, `#table-modal`, `#barcode-backfill-modal` and
`#table-qr-modal`.
- Each dialog has a fixed, centred frame in `app.css` (logical properties
  only), capped so it never covers the status bar.
- Stacking: z-index 560 for the modifier picker, 570 for the order-type
  prompt, 500 for the others.
- The shared `#ut-scrim` (#2873) sits behind them, and the status bar and
  the rail's Lock stay above it.
- `base.html` has one shared Escape handler for dialogs marked
  `data-ut-escape-close`. It fires `cancel` and then `close()`, and acts
  only on the topmost popup. It also has a Tab/focus trap for the same
  dialogs, added after review.
- The self-order kiosk's `#selforder-modal` stays modal on purpose: on a
  kiosk, those controls must be unreachable.
- New guard `scripts/ci/guard-no-showmodal.sh` with a self-test, wired into
  the `ci.yml` build job.
- Docs: CLAUDE.md "Offline-first" and ut-docs `coding-standards.md` §10.

## Findings (Fable)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The scrim blocks the pointer only. Tab left the non-modal dialog for the sale screen: with `#table-modal` open, Tab then Enter on a tile added a basket line. | **Fixed.** A Tab wrap and a `focusin` pull-back keep focus in the topmost opted-in dialog. The status bar, `.session-lock` and `#osk` may still take focus. New spec step (e): 8×Tab + 4×Shift+Tab stay inside every dialog. It failed with the trap removed and passes with it. |
| 2 | nit | A `shellcheck disable` directive had prose on the same line. | Fixed: the reason is now on the line above. |
| 3 | nit | The guard missed `.showModal.call(`, `.apply(` and `["showModal"]()`. | Fixed: the regex now covers them, with 2 new self-test cases (15 in total). The `//`-inside-a-string gap is documented. |
| 4 | nit | A garbled comment in `order-type-prompt-placement-2282.spec.ts`. | Reworded. |

Checked with no problem found:
- Stacking against `#elevation-modal`, `#hold-modal`, `#category-items-modal`, the OSK and `#pos-alert`.
- Escape interplay with `record-dialog.js`, `category-filter.js` and grid edit mode.
- The #2371 rules: closing without a choice counts as Cancel, and a wedge scan while the prompt is open re-arms the gated action.
- Kiosk isolation, i18n and RTL.

## Verification
- The new e2e spec was red then green, and the reviewer re-ran that
  independently: on main's `web/`, 6 failed (`:modal` true); on the branch,
  6 passed. The Tab-trap step was red without the trap and green with it,
  checked by the orchestrator.
- Dev run: 222 e2e tests passed across 42 specs touching these dialogs.
  After the review fixes: 33/33 (2097 + popup-scrim-2873 +
  order-type-prompt-placement-2282).
- Gates:
  - `gofmt -l .`: clean. `go build ./...` and `go test ./...`: pass.
  - `shellcheck scripts/ci/*.sh`: 0 issues.
  - Guards: no-showmodal (+ test), i18n, help-topics, help-drift,
    kiosk-engine, data-access, compliance-claims, competitor-naming,
    core-neutral and docs-shots all pass.
- Docs-shots: a full `make docs-shots` run changed no PNG. After the review
  fix, which only adds listeners, the surface hash was refreshed with
  `Docs-Shots-Unchanged: true`.
- Not run locally: `guard-deadcode-baseline.sh` fails in this container
  because the GTK headers are missing (Go is unchanged, and main's CI is
  green). Also not run: `guard-gobind-skip.sh` (no gobind) and a real
  PIN-session Lock (Lock is injected, as in `popup-scrim-2873`).
  No RTL- or phone-width-specific visual pass beyond the reviewer's 360×800
  geometry probe.

## Verdict
Safe to merge once CI is green.

## Deferred
- `#hold-modal` and `#pfand-modal` were `.show()` dialogs before this change
  and have no keyboard trap either. The same Tab-escape risk applies to them.
  Follow-up: ut-docs#3212.
