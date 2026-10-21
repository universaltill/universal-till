# Code review — edit mode, reorder and New vs the main-till lock (ut-docs#4418)

Date: 2026-10-21 · Branch: `fix/4418-edit-mode-main-till-lock` · Lane: `lane:cloud-41`
Author: Opus 5.5 (cloud build lane) · Reviewer: Fable (independent subagent, different model)

## What shipped

On an additional till every catalogue write travels to the main till and is
refused while it can't be reached. ut-docs#3605 disabled the forms
(`fieldset[data-main-till-lock]`); this closes the gaps it left on purpose and
the ones its Tester found:

- **Sale-screen quick-button edit mode** (`web/public/app.js`, `utTileJiggle`)
  does not open while locked: a hold or right-click shows a role=status note
  (`[data-jiggle-lock-note]`, existing key
  `sync.banner_replica_main_till_unreachable`) instead, and the refused hold's
  trailing pointer click is eaten so it doesn't ring the tile up. A lock that
  arrives mid-edit ends the mode (the existing refused-save path re-syncs a
  dirty reorder). A plain tap still sells — checkout never blocks.
- **list-reorder.js** (`/categories`, the Designer's category list) does not
  arm a drag or `move()` (keyboard Alt+Arrow) while locked.
- **New buttons** — catalog toolbar `#item-form-add-btn`, item-dialog head
  `#item-form-reset`, `list_header`'s New (new optional
  `mainTillLock`/`mainTillLocked` pair, used by /categories), Designer
  `#designer-cat-new` — now sit in `fieldset[data-main-till-lock]`.
- **Designer** gets its own lock note (`data-main-till-away`).
- Mechanism: `base.html`'s `applyMainTillLock` also toggles
  `data-main-till-locked` on `<html>` (live state; the sale screen renders
  none of its own) and on `[data-main-till-gate]` elements a page rendered
  locked (Designer replica root, both reorder lists), and fires
  `ut-main-till-lock` on `window`.
- Manual: `web/help/{en,de,tr,ar,fa}/sell.md` (edit mode) and `multitill.md`
  item 7 gained the behaviour; docs-shots regenerated.

## Findings (Fable review of the WIP snapshot)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `<html data-main-till-locked>` never cleared when the chip disappears (till promoted / stops following): `followChip` only acted on a non-empty state, and the attribute outlives boosted navigations → edit mode and drags refused on the new main till until a hard reload. | **Fixed**: `followChip` lifts the lock when the chip goes away; e2e step added and seen failing without the fix. |
| 2 | minor | `eatLockClick` could survive a refused hold with no tile click (slid off, released elsewhere) and then eat a later **keyboard** Enter/Space on a tile — a missed sale. | **Fixed**: only pointer clicks (`e.detail !== 0`) are eaten, and the flag is cleared right after `pointerup` (setTimeout 0, after the trailing click) and on `pointercancel`. |
| 3 | minor | `contextmenu` refusal path didn't set `eatLockClick` (asymmetric). | **Fixed** (safe given #2). |
| 4 | minor | The note sits inside the `.products` root that `buttons-changed` swaps, so it comes back hidden while still locked. | **Accepted**: the next refused hold re-shows it; the status-bar chip always says it. |
| 5 | nit | de wording "einen Hinweis darauf"; ar stray fatḥa, mixed "الصندوق/الجهاز الرئيسي". | **Fixed** (de reworded; ar fatḥa removed, sentence uses الجهاز الرئيسي like the file's own lock paragraph). |

Reviewer also checked and found OK: click ordering for a <500 ms tap; mid-edit
lock exit path; initial-load window (sub-second, mount polls on load); template
scope of `EditMode`/`SyncUnreachable` (`internal/ui/buttons.go`); fieldsets are
`display: contents` and no JS/Go test depends on the moved buttons' DOM
position; a11y re-announce; no physical CSS.

## Verified

- New `e2e/tests/edit-mode-main-till-lock-4418.spec.ts` (3 tests): all failed
  on `main`'s web/ code (re-verified by reverting the fix), the reorder
  assertion fails with only `list-reorder.js` reverted, the F1 step fails with
  only `base.html` reverted; all pass with the fix.
- Regression specs: `sell-tile-jiggle-mode-2339`, `sell-all-grid-jiggle-2534`,
  `categories-record-dialog-main-till-lock-3605`, `designer-drop-no-reload-3074`
  — 17/17 with the new spec.
- `go build ./...`, `go vet`, `go test ./internal/ui/... ./internal/pages/...`;
  `guard-i18n`, `guard-help-drift`, `guard-rtl-logical-css`, every `ci.yml`
  build guard locally (deadcode/shellcheck-version unrunnable here: toolchain
  go1.26 / no shellcheck binary — CI runs them).
- **Not verified on real touch hardware** — gestures are Playwright's mouse
  pointer (Chromium). Not looked at on a real two-till setup; the lock is
  driven through the real chip follower as in #3605's spec.

## Verdict

Safe to merge once CI is green.
