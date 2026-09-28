# Review: move a quick button to another category (ut-docs#2465)

**Date:** 2026-09-28 · **Branch:** `feat/2465-move-tile-category` · **Built by:** Opus 5.5 (dev subagent) · **Reviewed by:** Fable (independent subagent)

## What shipped
Product owner: in the sale screen's edit mode, dragging an item onto another category must change that item's category.

- **`CatalogRepo.SetItemCategory`**: one transaction. An empty category means Uncategorised (NULL). An unknown or inactive category returns `ErrCategoryNotFound`; an unknown, inactive or blank item returns `ErrItemNotFound`. It bumps `updated_at`. The admin-sync (migration 023) and sell-screen (047) triggers on `items` fire exactly as they do for a catalog-editor save, so other tills and the cloud's content-hash push pick the change up with no extra wiring.
- **`POST /api/buttons/recategorize`** (`item_id`, `category_id`): the same shape as `/api/buttons/reorder`.
  - `requirePrimary` (a replica gets a localized 409), then `checkOrElevate("catalog_management")`: a cashier gets the PIN prompt, which carries both fields.
  - Audited as `buttons_recategorize` when a PIN approved it.
  - On success: 204 + `HX-Trigger: buttons-changed` + `X-UT-Sell-Version`.
  - On the demo allow-list.
- **Sale screen and Designer replica (`app.js` utTileJiggle):**
  - Drag: a tile dragged over another category's tab marks it as a drop target (dashed inset outline, tint and an arrow, so not colour alone). Letting go there saves any pending reorder, then moves the item.
  - Keyboard, touch and screen-reader path: a **Move to category** badge in the fourth tile corner opens `#tile-move-dialog`. The dialog lists the other categories, including the ones behind "…".
  - Hidden for a Locked (cashier) session. Not in the all_filter_chips All grid.
- New `folder-input` icon (httpx + uislot mirror). 6 keys in en/ar/fa/tr. Manual: `till-designer` step 2 and the `sell` "Rearranging" section in en/de/ar/fa/tr; docs-shots regenerated.

## Findings (Fable): verdict "safe to merge", no blockers
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Passing over sibling tiles on the way to a tab reshuffled the grid and caused an extra, unintended reorder write | **Fixed.** `startDrag` records the cell's home slot and the prior `dirty`. A tab drop puts the cell back and keeps only a reorder that was pending before the drag. |
| 2 | nit | Audit `entity_type` is the shared `shortcut_button`, though the entity is an item | Accepted: consistent with the sibling button audits. The payload carries `item_id`/`category_id`. |
| 3 | nit | The elevation summary falls back to the raw category id if its lookup fails | Accepted: escaped, and only on a DB error |
| 4 | nit | `aria-controls` on a hidden badge where no dialog renders | Accepted: `display:none`, no user impact |
| 5 | nit | A scrim tap closes the dialog and also leaves edit mode | Accepted as pre-existing: shared with the category overflow sheet |
| 6 | nit | Repo test didn't pin `sync_admin_version` | **Fixed:** asserted |
| 7 | note | A non-elevated manager's move writes no audit row | Accepted: same as reorder/hide and the catalog editor |

## Verified beyond unit tests
- The reviewer re-verified TDD in a separate worktree. With the source reverted, the repo tests fail to build (`SetItemCategory undefined`), the route tests fail with 404, and the render test fails on the missing badge. All pass once restored.
- e2e `sell-tile-move-category-2465.spec.ts` (4 tests):
  - drag onto a tab: the pending reorder POST is sent before the recategorize POST, and the move survives a reload;
  - the badge dialog by keyboard: Escape closes only the dialog, and focus lands on Done;
  - the Designer replica;
  - an RTL (fa) smoke test.
- Cashier (auth project): no badge and no dialog, and the POST gets the PIN prompt.
- The new spec and `sell-tile-jiggle-mode-2339` were re-run after fix 1: 7/7. Before the fix the related specs passed 50/50 and the auth project 30/30.
- Screenshots looked at: edit mode, the drop target and the dialog, in en and fa, at 1024×600 and 360px. Four badges in four corners, nothing clipped. At 360px only one tab fits, so the dialog is the path there.
- **Not verified on real touch hardware / WebKitGTK**: every gesture was Playwright's synthetic pointer in Chromium. Dark theme not screenshotted.

## Gate
- `gofmt` clean, `go build`, `go vet`, `golangci-lint` 0 issues.
- `go test ./...`: all packages pass. `internal/pages` and `internal/plugins` pass the way CI runs them (no `-race`).
- Every ci.yml build-job guard passes, except that `shellcheck` is not installed locally (no shell script touched).
- The docs-shots surface hash was refreshed after fix 1, which changes behaviour only, not pixels.

**Verdict:** safe to merge. The de/es language packs need the 6 new keys right after core merges.
