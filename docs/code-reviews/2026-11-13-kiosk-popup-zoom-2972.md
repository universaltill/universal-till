# Review: self-order kiosk gets the shared popup zoom (ut-docs#2972)

**Change.** Branch `feat/2972-kiosk-popup-zoom`, one WIP commit. Reviewed at
local HEAD `44db110`. The pushed `origin/feat/2972-kiosk-popup-zoom` is still
at `cc8ab35`; see F1.

- **Move.** About 650 lines of `web/ui/layouts/base.html`'s first inline
  script move into the new `web/public/ut-motion.js`. That code covers
  `UT.fxLevel`/`motionOff`, `trackZoom`/`finishZooms`, the mid-transition
  pointer hand-over, the origin recorder, the ADR-0122 §5 popup zoom, the
  scrim, the Escape/focus trap, the showModal/show wrapper and
  `UT.vtWatchdog`.
  - `base.html` and `web/ui/pages/self_order_shop.html` load the file
    synchronously as their first `<script>`, with
    `?v={{ assetv "public/ut-motion.js" }}`.
  - The file is added to `httpx.HeadAssets`, so it is part of the
    persistent-shell signature.
  - The page-zoom helpers (`zoomTo`/`zoomRemember`/`zoomRecall`) and
    `pageswap`/`pagereveal` stay in `base.html`. They read
    `UT.viewport`/`UT.onScreen`, which `ut-motion.js` now exports.
- **Clear after the shrink.** `#selforder-modal` carries
  `data-ut-clear-on-close`. `UT.popupClosed` records the dialog's children
  when it closes. `popupClose`'s `done` removes only those children, and only
  while the dialog is still closed. With no shrink running (Light, reduced
  motion, no box), the dialog is emptied straight away. The
  `close-clear` action in `inline-actions.js` no longer empties the dialog
  at its close site.
- **Tests.**
  - The Go guards are repointed to `ut-motion.js`.
  - New guards: `TestStandaloneDialogPagesLoadSharedMotion` and
    `TestClearOnCloseRunsAfterShrink`.
  - `TestNoDialogIsEmptiedAtItsCloseSite` now also checks `self_order_*`
    files, `closeDialog(...)` and `replaceChildren(`.
  - New e2e spec: `e2e/tests/self-order-popup-zoom-2972.spec.ts`.
  - `self-order-age-restricted-picker-3396` now emulates reduced motion
    because it measures layout.
  - The docs-shots surface hash is refreshed.

**Pipeline.** BA, Architect, Dev (Sonnet), Tester and UX notes are in the
2972 scratchpad. The reviewer was an independent Opus 5.5 subagent.

## Verbatim check

I extracted the removed `base.html` lines (both hunks, 653 lines) and ran
`diff` against `ut-motion.js`. The only differences are these, and all are
intended:

1. **IIFE wrapper and header comment.**
2. **`UT.fxLevel`.** It is now parsed once from the `<html>` `fx-*` class
   instead of the templated `'{{ fxlevel }}'`. Both come from the same
   `fxlevel` template value at render time, and no code changes that class
   at runtime. The persistent shell never re-ran the head script before
   either, so the behaviour is the same.
3. **`clearKids` in `popupClose`'s `done`.**
4. **The `data-ut-clear-on-close` block in `UT.popupClosed`.**
5. **The `UT.viewport`/`UT.onScreen` exports.**
6. **Position of `UT.vtWatchdog`.** It moved from after `zoomRecall` to the
   end of the file. It is still defined before any caller and still sees
   the `activeVT` closure.

Other checks on the move:

- The remaining `base.html` code uses no closure variable from the moved
  block. I checked `activeVT`, `zoomTap`, `skippedByTapAt`, `zooms`,
  `popups`, `livePopups` and `ORIGIN_SEL`.
- `ut-motion.js` has no top-level early `return`, so every export always
  runs.
- `node` parses the file.

## Findings

| # | Sev | Finding | Status |
|---|---|---|---|
| F1 | medium (process) | The pushed `origin/feat/2972-kiosk-popup-zoom` (`cc8ab35`) still has `closeDialog(d); d.innerHTML = '';` in `inline-actions.js` `close-clear`. That is the bug this card fixes, and both new guards fail on it. The reviewed local HEAD `44db110` has the fix. Push the amended HEAD with `--force-with-lease` before the PR. | **Fixed.** The orchestrator force-pushes the squashed commit, which carries the fix. |
| F2 | low | `ut-motion.js` is now a separate request, and more than motion depends on it: Escape-to-close (`data-ut-escape-close`), the focus trap, the scrim, `UT.vtWatchdog` and the kiosk's after-shrink clear. If it fails to load (404 or a broken deploy), two more things break. First, `base.html`'s page-zoom script returns at `if (!UT.onScreen) return;` (base.html:93). Second, the shell's `htmx:beforeTransition` handler throws a TypeError on `UT.motionOff()` (base.html:698) on every boosted navigation. The comment at base.html:89-91 ("Without it only the motion is lost") is therefore wrong. The risk is low, because the file is vendored and served by the till itself, which is offline-safe. Fix: correct the comment and guard line 698 with `UT.motionOff && UT.motionOff()`. | **Fixed.** The comment now lists everything the file holds. `htmx:beforeTransition` uses `(UT.motionOff && UT.motionOff())` and returns before the zoom origin when `UT.takeOrigin`/`UT.zoomTo` are missing. The `startViewTransition` wrapper calls `UT.vtWatchdog` only if it exists. The Go pins are updated to the new literal. I checked the remaining shell-script `UT.*` calls (`ready`, `reload`, `noteNav`): that script defines them itself. |
| F3 | low | No permanent test covers reopening during the closing shrink: a tile tap mid-shrink, or content swapped in mid-shrink. I proved both cases with a throwaway spec (see Verification). They are the main races of the clear-after-shrink design, so add them to `self-order-popup-zoom-2972.spec.ts`. | **Fixed.** Two Full-level tests were added. In the first, a tile tap during the shrink reopens the dialog; the new picker (tagged `data-fresh`) survives the old shrink's end and its backstop. In the second, an `htmx.ajax` innerHTML swap lands during the shrink and is kept after it ends. |
| F4 | nit | Stale "base.html's first script" wording in comments: `web/public/app.js:3495`, `app.js:3779`, `web/public/inline-actions.js:273`, `web/public/record-dialog.js:322`. The same wording is in failure messages at `internal/pages/transitions_test.go:850,854,865,879`, which now read `ut-motion.js`. | **Fixed.** All of them now name `ut-motion.js`. |
| F5 | nit | `TestBaseHTMLZoomTapFinishesAndRecordsOrigin` now counts the origin recorder only in `ut-motion.js`. A second `recordOrigin` re-added in `base.html` would no longer fail. Count across `base.html` + `ut-motion.js`. The prototype-wrapper "exactly once" guard has no such gap, because `webFiles` walks `web/ui` too. | **Fixed.** The check now counts across `ut-motion.js` + `base.html`. |
| F6 | nit | The `TestNoDialogIsEmptiedAtItsCloseSite` regex catches `.innerHTML =` and `replaceChildren(` but not `textContent = ''`/`innerText = ''` after a close. | **Fixed.** The new `closeThenEmptyRE` adds `textContent =` and `innerText =`. The new `TestCloseThenEmptyPatternCoversAllForms` pins all five forms and one negative case. I checked red-first (see Verification). |
| F7 | info | `UT.popupClosed`'s clear uses `popupState()`. With no `WeakMap` (`popups === null`), that throws inside the `try`, and the dialog is never cleared. Every supported engine has `WeakMap`. | accepted |
| F8 | info | Behaviour change on the kiosk: Escape (native modal cancel) now also empties the picker after the shrink, because the attribute is on the dialog. Before, only `close-clear` emptied it. This is harmless, because every open swaps fresh markup in first. | accepted |

Rules from `CLAUDE.md` I checked, with no issue found:

- **showModal:** the kiosk modal keeps its `showmodal:allow`;
  `guard-no-showmodal` is green.
- **Kiosk isolation:** no Go route file changed; `guard-kiosk-engine` is
  green.
- **i18n:** `ut-motion.js` has no user-facing strings; `guard-i18n` is green.
- **RTL:** `--ut-closing-is` stays logical; `guard-rtl-logical-css` is green.
- **native confirm:** none added.
- **assetv:** used on both tags and in `HeadAssets`, which the shell
  signature guard checks.
- **Persistent shell (ADR-0098):** `self_order_shop.html` is not a
  base.html page, so it always loads in full. Boosted swaps never re-run head
  scripts, and the wrapper keeps its `native.utWrapped` re-install guard. A
  till upgrading from the old inline script gets a new shell signature and
  so a full load, which means the code is never installed twice.
- **Moving inline JS to `'self'`:** this also helps the report-only CSP.
- **Recurring bugs:** no Go file write and no cwd-relative path in the
  change. The tests use the existing `chdirRoot`.

## Verification (reviewer)

- **Go:**
  - `go build ./...` passes.
  - `go vet ./internal/pages/... ./internal/httpx/...` is clean.
  - `go test ./internal/pages/... ./internal/httpx/... ./web/...` is all
    ok.
  - `gofmt -l internal/` prints nothing.
- **TDD re-verify,** in a separate worktree at `44db110`, since removed with
  `git worktree remove --force`:
  - I re-added `d.innerHTML = '';` after `closeDialog(d);` in `close-clear`.
  - **Both tests failed.** `TestNoDialogIsEmptiedAtItsCloseSite` failed with
    "empties a dialog right after .close() (\"closeDialog(d); d.innerHTML
    =\")". `TestClearOnCloseRunsAfterShrink` failed with "close-clear must
    not empty the dialog itself".
  - **After restoring the fix, both passed.**
- **e2e** (Chromium, `--trace off`):
  - `self-order-popup-zoom-2972` and `popup-zoom-2944`: 17/17 passed.
  - Every `self-order*`/`kiosk*` spec plus
    `manual-print-customer-language-4714`: 21/21 passed. Two of these open
    the kiosk modal at Full motion in a fresh context, so they now run the
    zoom.
  - Till motion, scrim and Escape specs: 92/92 passed. These are
    confirm-dialog-escape-focus-5275, effects-level-2859,
    in-panel-dialog-transitions-2338, page-transitions-2223,
    page-zoom-2942, pair-confirm-escape-5275, phone-input-no-zoom-3350,
    phone-lock-over-scrim-6118, popup-scrim-2873 and tree-pane-zoom-2943.
- **Races** (throwaway spec in the review worktree, not committed): 2/2
  passed.
  - **Tile tapped 300 ms into a 3 s closing shrink.** Exactly one
    `.modifier-picker` is shown. 3.6 s later the dialog is still open, not
    closing, still filled, and shows the right item.
  - **Markup inserted into the dialog mid-shrink with no pointer** (so no
    `finishZooms`). After the shrink, the inserted node survives and only
    the old children are removed.
- **Guards** green: no-showmodal, no-native-confirm, i18n, rtl-logical-css,
  kiosk-engine, docs-shots (surface `1c5c6e03…` fresh), help-topics,
  fixed-dates, compliance-claims, wip-commits.

### Re-verification of the review fixes (F2–F6)

These are uncommitted Dev fixes in the main checkout. I read the diff.

- **F6, red-first,** in a throwaway worktree at HEAD with the fix diff
  applied, since removed with `git worktree remove --force`:
  - I changed `close-clear` to `closeDialog(d); d.textContent = '';`.
    `TestNoDialogIsEmptiedAtItsCloseSite` **failed** with "web/public/inline-actions.js
    empties a dialog right after .close() (\"closeDialog(d); d.textContent
    =\")".
  - I added a planted partial with `closeDialog(d); d.innerText = "";`. The
    test **failed** on it as well.
  - With HEAD's old regex, the same `textContent` edit **passed**, which
    confirms the gap was real.
  - `TestCloseThenEmptyPatternCoversAllForms` passes.
- **Go:** `gofmt -l internal/` prints nothing, and
  `go test ./internal/pages/... ./internal/httpx/...` is all ok.
- **Docs-shots:** `guard-docs-shots.sh` is fresh (surface `3b10b365…`).
- **e2e:** `self-order-popup-zoom-2972.spec.ts` (`--trace off`), 11/11
  passed, including the two new race tests.

## Verdict

**Approve.** The move is verbatim apart from the intended edits. The
clear-after-shrink is race-safe: it removes only the recorded nodes, only
while the dialog is closed, and a reopen cancels it.

- **F2–F6:** fixed and re-verified.
- **F1:** resolved by the orchestrator's force-push of the squashed commit.
- **F7 and F8:** accepted.

## Deferred

None beyond the open findings above. The ADR-0122 "Location note" is on the
ut-docs branch `docs/2972-ut-motion-pointer` and must merge with this change.

Pre-flight: web/help n/a (motion only; no step, label or screen changed, and the docs-shots manifest is refreshed and fresh); reference updated (ADR-0122 location note, ut-docs `docs/2972-ut-motion-pointer`); en.json n/a (no keys); README n/a (not stale); plan/platform/manifest rows n/a; review record: this file; follow-ups: none (F2–F6 fixed on this branch; F1 resolved by the force-push).
