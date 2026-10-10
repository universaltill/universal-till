# Review — remaining non-modal till dialogs trap Tab (ut-docs#4028)

**Date:** 2026-10-10 · **Lane:** lane:cloud-24 · **Branch:** `fix/4028-dialog-focus-trap`
**Author model:** Sonnet (Dev + Tester subagent) · **Reviewer:** Opus 5.5 (independent subagent, separate worktree)

## What shipped

Follow-up to ut-docs#3212, which added `data-ut-focus-trap` to base.html's
keyboard trap (Tab/Shift+Tab wrap inside the topmost opted-in `.show()`
dialog; focus landing behind it is pulled back; status bar, Lock, `#osk`
and `#bugreport-panel` stay focusable). The older `.show()` dialogs this
card lists still let Tab walk to the page behind `#ut-scrim`. They now opt
in, **trap only**: no dialog gained or lost Escape-to-close.

- `tables.html` `#table-add-modal`
- `catalog.html` `#item-form-modal`, `#import-modal`, `#tax-codes-modal`
- `inventory.html` `#stock-dialog` (its hand-wired Escape is unchanged)
- `partials/basket.html` `.age-check-sheet`, `.shrinkage-sheet`
- `partials/buttons.html` `#category-overflow-dialog`. Its Alpine
  `trapOverflowTab` already wrapped Tab; the attribute adds the focusin
  pull-back. Alpine's handler preventDefaults when it wraps and base.html's
  window handler bails on `defaultPrevented`, so focus never moves twice.

**Left out on purpose:** the `record_dialog.html` record dialogs, because
`record-dialog.js` already traps every `[data-record-dialog]`, and
`#tile-move-dialog`, which already traps by hand. `#stock-dialog` has the
`record-dialog` *class* but not `data-record-dialog`, so it is not trapped
twice.

Tests: a new `ut-docs#4028` block in
`e2e/tests/showmodal-statusbar-reachable-2097.spec.ts`, with 6 tests using
the existing `assertFocusTrapped`. They cover the Tab/Shift+Tab walk, the
pull-back from the page behind, and that the status bar can still take focus.

`web/help/img/manifest.json`: the surface hash was refreshed with
`update-docs-shots-surface-hash.sh`. An added attribute changes no rendered
pixel.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | An auto-opened `.age-check-sheet` (tender gate) focuses its first control, the `?` help link. That was already true with `.show()` before this change, but the trap's wrap and pull-back now target the link too, so a stray Enter opens /help mid-sale. The basket is server-side, so no data is lost. | Deferred: ut-docs#4077. Autofocusing Accept would be worse, since a stray Enter would accept the age check. |
| 2 | minor | `record-dialog.js`'s own focusin pull-back exempts only `#osk`, not the status bar or Lock. This predates the change. | Deferred: ut-docs#4078 |
| 3 | nit | The overflow test was titled "exactly one step" / "not double-moved", but either Tab handler alone lands on the same control, so it couldn't catch a double move. | Fixed: retitled, and the comment now says what it checks. The pull-back assertion is the one that fails without the attribute. |
| 4 | nit | The throwaway age-restricted item's EAN-13 had a wrong check digit. | Fixed (`5009990040282`) |
| 5 | nit | For `#item-form-modal` and `#stock-dialog`, the fixed 8×Tab walk never reaches the wrap point (too many controls). | Accepted: the pull-back assertion covers them, and the reviewer confirmed it fails without the attribute. |
| 6 | nit | `category_filter_popover.html`'s category-filter dialog is still untrapped. It was not in this card's list. | Deferred: ut-docs#4079 |

Checked and fine: a basket swap that removes an open sheet leaves no stale
trap, because `escTop()` calls `scrimSync()`, which drops disconnected,
closed or modal entries. No scan-input refocus fights the trap
(`scanFocusLater` runs only on New Sale). The payment overlay's tabindex
sweep doesn't touch the sheet. Nothing inside `#item-form-modal` renders
outside it (viewfinder, icon and colour pickers, file inputs). Nested
confirm and elevation dialogs carry the trap themselves. Escape behaviour is
unchanged, and the test asserts it for `#stock-dialog`. There are no new
strings, no CSS and no `showModal`. The help manual needs no change: this is
keyboard-only, the same conclusion as the #3212 review.

## Verification

- **TDD:** the Dev run before implementing had 10 passed and 6 failed: all
  six new tests, failing on the Tab walk or the pull-back. The reviewer
  re-verified independently in a separate worktree:
  - templates reverted: all 6 new tests failed on trap assertions;
  - the attribute removed from `#import-modal`/`#tax-codes-modal` only:
    failure at `#import-modal`;
  - removed from `#tax-codes-modal` only: failure on the pull-back;
  - restored: everything passed.
- **The spec file:** 16/16 pass after the review fixes; `-g 4028
  --repeat-each=2` gave 12/12.
- **Related specs:** 8 of them, 50/50 (`tables-tap-to-add-1025`,
  `catalog-item-form-1956`, `items-shell-catalog-import-taxcodes-dialog-2095`,
  `inventory-cost-currency-decimals-1282`,
  `sale-screen-category-strip-overflow-2307`, `codeless-item-shortcut-1459`,
  `popup-scrim-2873`, `catalog-item-modifiers-tab-2211`).
- **Go and guards:**
  - `gofmt -l .` is clean and `go build ./...` builds. `go test ./...`: 89
    packages pass. `internal/plugins` hit the default 10-minute timeout in
    this container (WASM compiles). CI runs it in its own step with a longer
    timeout, and the diff touches no Go.
  - Every `ci.yml` build-job guard passes locally, with two exceptions that
    are container tooling: `guard-deadcode-baseline` (the deadcode binary
    was built with go1.26) and `guard-shellcheck-version` (no shellcheck
    binary). Neither is touched by this diff, and CI runs both.
- **Not checked:** real touch hardware, an RTL locale, the on-screen
  keyboard with these dialogs open, and whether a wedge scanner's Enter
  reaches the focused element (that question is on #4077). The overflow
  test opens the dialog with `.show()` rather than reaching More through
  real overflow at 1024×600.

**Verdict:** safe to merge.
