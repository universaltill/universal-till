# Review — cloud quick-button layout keeps hidden rows in their slots (ut-docs#2709)

- **Card:** ut-docs#2709 (lane:cloud-24), complexity medium. Built by Opus 5.5; reviewed by Fable in an isolated worktree.
- **Branch:** `fix/2709-layout-hidden-slots`

## What shipped

`cloudSetQuickButtonLayout` (the `set_quick_button_layout` directive hook, `internal/pages/cloudsync_wire.go`):

- The cloud only sees visible quick buttons (`remoteQuickButtonsReport` → `LoadButtons`), so its payload lists exactly those.
  The hook still refuses a payload that doesn't cover exactly that set. It now reads it from `LoadGridButtons` (hidden rows included, `!Hidden` = visible).
  A payload naming a hidden row's barcode is now refused with its own message ("is hidden on this till's sell screen"). Before, it got the generic "not one of this till's quick buttons".
- The write covers every row edit mode shows. `layoutWithHiddenSlots` keeps each hidden row at its current index and fills the other indexes with the payload in order.
  `UpdateOrder` then renumbers the whole list 0..n-1.
  Before, only the listed rows were rewritten. A hidden row (kept on purpose to hold its slot, ut-docs#2541) stayed on its old `sort_order` and collided with a visible tile's new one.
  Edit mode then ordered that pair by label instead of by the owner's layout. A till that already has a collision is fixed by the next layout directive.
- `ShortcutsRepo.LoadButtons` doc comment updated to match.

**Split out:** the card's snapshot half (items' `sell_screen_hidden`/`sell_screen_removed` in the till→cloud catalog snapshot, shown and restorable on my.) spans universal-till + ut-cloud + ut-my-shop. It is filed as **ut-docs#3015** (related #2562).

## Tests

- `TestCloudSetQuickButtonLayout_HiddenRowKeepsSlotNoCollision`: 3 visible rows plus a hidden row in slot 1. The directive reorders the visible ones. The test asserts that no two rows share a `sort_order`, that the order is `b3,h1,b1,b2`, and that `LoadGridButtons` (edit mode) returns the same order.
  Written first. It failed on the old code with `sort_order 1 shared by "b1" and "h1"`.
- `TestCloudSetQuickButtonLayout_HiddenBarcodeRefused`: a payload naming the hidden barcode is refused with the hidden-specific message, and nothing is written.
  Tightened after review. I checked that it fails when the hidden branch is disabled.

## Review findings (Fable)

The reviewer ran build, vet and the pages/data/cloudsync tests. It also re-ran the TDD check itself: with the fix reverted, the test fails with the collision; restored, it passes.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The hidden-refusal test only asserted `err != nil`, so it also passed on the pre-fix code | **Fixed:** it asserts the message and that nothing was written; verified against a disabled branch |
| 2 | minor | Stale `LoadButtons` doc comment said the directive is validated against it | **Fixed** |
| 3 | nit | `layoutWithHiddenSlots` has no bounds guard | **Accepted:** validation guarantees `len(visible)` = the visible count (barcode is the PK, payload duplicates, unknowns and missing entries are all refused) |

Checked with no issue found:
- Directives apply one after another in one goroutine per tick. A local edit between the read and the write still ends in a full renumber (a new row lands at MAX+1).
- Removed rows don't exist (`RemoveFromSellScreen` deletes them). Soft-deleted items are never reactivated, so their stale `sort_order` can't be reached.
- ut-cloud stores the result message opaquely and nothing matches on the refusal text.
- The local Designer drag route (`ButtonStore.UpdateOrder`) already posts hidden tiles and renumbers everything, so it doesn't have this bug.

## Beyond automated tests

- No visible surface changed: the directive only changes stored order, and edit-mode order is asserted through `LoadGridButtons`. I did not drive the Designer in a browser or run e2e, because no e2e covers cloud directives. `web/help/img/manifest.json` got a surface-hash refresh only (`Docs-Shots-Unchanged`).
- No docs went stale: `manage-shop-catalog-api.md` and the ut-cloud `api-reference.md` list only the directive type and payload, and neither changes.

## Gate

gofmt, `go build ./...`, `go test ./... -race`, and the ci.yml build-job guards. Two local-environment exceptions:
- `guard-deadcode-baseline.sh` flags `internal/logging/file.go`. That fails the same way on `origin/main` here, because the desktop root is skipped without GTK headers.
- `shellcheck` isn't installed here. No shell scripts were touched.

## Verdict

Safe to merge.
