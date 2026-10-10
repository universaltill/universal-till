# Review — older non-modal till dialogs trap Tab (ut-docs#3212)

**Date:** 2026-10-09 · **Lane:** lane:cloud-41 · **Branch:** `fix/3212-dialog-focus-trap`
**Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped

`.show()` dialogs have no keyboard inertness: `#ut-scrim` blocks the pointer
only. ut-docs#2097 added a Tab trap in `web/ui/layouts/base.html` for dialogs
opted in with `data-ut-escape-close`; the older non-modal dialogs never had
one, so with e.g. Hold Sale open, Tab walked to a product tile or Pay behind
the scrim and Enter acted on it.

- `base.html`: the trap (`escTop()`, used by the Tab keydown handler and the
  focusin pull-back) now also accepts a new `data-ut-focus-trap` attribute —
  the trap **without** Escape-to-close. `data-ut-escape-close` still implies
  both; the Escape handler is unchanged, so no dialog gained Escape.
- Opted in: `#hold-modal`, `#parked-orders-modal`, `#category-items-modal`
  (index.html), `#pfand-modal` (menu.html), `#elevation-modal` and
  `#confirm-modal` (both their OOB-swapped partials and base.html's
  placeholders — the swapped-in element's attributes are the ones that count).
- `inline-actions.js` `show-keep-focus` (the pfand opener, its only caller):
  it restored focus to the menu tile behind the dialog; the trap's pull-back
  then moved focus into `#pfand-amount` — the #1248 regression
  (`deposit-refund-osk-1248.spec.ts` caught it during Dev). For a trapped
  dialog it now leaves focus on `<body>`; the next Tab enters the dialog.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix | Native `show()` focuses the new dialog before `scrimTrack` makes it topmost, so a dialog opened over a trapped one (category popup → modifier picker / order-type prompt) had its initial focus pulled back behind it. | **Fixed:** the show/showModal wrapper sets `UT.dialogOpening` around the native call; the focusin pull-back ignores focus landing inside it. e2e step added. |
| S2 | should-fix | `escFocusables` omitted `summary`; `#parked-orders-modal`'s Move-table `<details>` toggle made Tab from it jump to the list's first element. | **Fixed:** `summary` added to the selector. e2e step added. |
| N1 | nit | `show-keep-focus` comment named the OSK; the risk is the native keyboard (osk.js opens on click only). | **Fixed** (comment). The duplicated attribute pair stays — two call sites, small. |
| N2 | nit | The `#category-items-modal` e2e opens the empty dialog (only Close focusable). | Accepted: the trap mechanism is shared; the S1 step exercises a real stacked open over it. |
| N3 | nit | `#bugreport-panel` (no-scrim, meant to stay usable while reproducing) is not exempt from the pull-back. | **Fixed:** added to `escAllowed`. |
| N4 | nit | `sbFocus` assertion also passes when the status bar has no focusable. | Accepted (status bar content varies by till state). |

Out of scope, filed: **ut-docs#4028** — the remaining untrapped scrim dialogs
(`#table-add-modal`, catalog item/import/tax-code forms, `#stock-dialog`,
record dialogs, age-check / shrinkage sheets, `#category-overflow-dialog`).

## Verification

- TDD: Go `TestElevationPrompt_/TestConfirmPrompt_/TestBaseLayout_…FocusTrap`
  and the e2e `#3212` block failed before the fix (reviewer re-verified by
  reverting `web/` in a separate worktree: 3 Go + 3 e2e failures, all on the
  missing trap); the S1/S2 steps were each confirmed failing with their hunk
  removed and passing with it.
- e2e: the whole `showmodal-statusbar-reachable-2097.spec.ts` plus every spec
  touching these dialogs (hold, pfand/OSK 1248/1249/1284, parked orders /
  table move, category popup 2499/2500/3073, popup scrim 2873, payment
  overlay OSK 1385, compact tender, jiggle mode) — all green.
- Looked at screenshots at 1024×600: Hold dialog after 4× Tab (focus back on
  its name field), Deposit refund on open (nothing focused) and after one Tab
  (focus enters the Amount field). Layout unchanged. Not checked at 360px or
  in RTL: the change adds no markup, CSS or strings — keyboard behaviour only.
- Guards: all `ci.yml` build-job guards pass locally; docs-shots needed a
  surface-hash-only refresh (no manual screenshot shows these dialogs).
  `go build`, `gofmt`, `golangci-lint` clean; `go test ./...` green except
  `internal/plugins` hitting the default 10-min timeout (CI runs it with 20m;
  untouched by this diff).
- Manual: no help topic describes keyboard focus in these dialogs; no change.

**Verdict:** safe to merge.
