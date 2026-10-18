# Review: the category filter popover traps Tab (ut-docs#4079)

Date: 2026-10-18 · Branch `fix/4079-category-filter-focus-trap` · Card complexity: easy
Built by the lane:cloud-41 session: a Sonnet Dev subagent, per MODEL-ROUTING for easy cards. The independent review was a fresh-context Opus 5.5 subagent.

## What shipped
- `web/ui/partials/category_filter_popover.html`: the popover dialog now carries `data-ut-focus-trap`.
  - The dialog is the non-modal `.show()` one over `#ut-scrim`, used on /catalog and /inventory.
  - `#4028` gave the same attribute to every other such dialog but missed this one.
  - base.html's shared trap now wraps Tab and Shift+Tab inside the popover. It also pulls focus that lands on the page behind back in.
  - The status bar, Lock and the on-screen keyboard stay reachable.
  - Escape is still handled by `bindPopover` in `category-filter.js`. That file has no hand-written Tab trap, so nothing competes with base.html's.
- `e2e/tests/showmodal-statusbar-reachable-2097.spec.ts`: a new test in the `#4028` block. It runs `assertFocusTrapped` on `#catalog-category-filter-dialog` and on `#inventory-category-filter-dialog`, then checks that Escape still closes each one.
- `e2e/tests/catalog-inventory-category-filter-2119.spec.ts`: both tests now close the popover before typing in `#catalog-search` / `#stock-search`, and reopen it to tap "All categories".
  - Before, both tests filled the search box behind the open popover.
  - A real user can't do that. A tap there hits the scrim, which closes the popover; the reviewer's `elementFromPoint` returned `ut-scrim` for both the search box and the trigger.
  - Playwright's `fill` doesn't hit-test, and the new focus pull-back broke those fills.
  - The tests still check that search combines with the selected category, and that a search with no match under that category shows the no-matches row.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | — (Dev) | With the attribute, the two 2119 tests failed after `search.fill('')`, because the fill happened behind the open popover | Fixed: those tests now close the popover first, as a user must |
| 2 | nit, pre-existing | Escape is a keydown listener on the dialog. With the popover open and focus on the status bar (which the trap allows), Escape doesn't close it. `data-ut-escape-close` would make Escape work from anywhere | Accepted. Escape staying with `bindPopover` is deliberate, and the close button and scrim tap still work. The new test focuses the close button before pressing Escape |
| 3 | nit, pre-existing | The "compose with search" step only checks that Bread is visible and Cornflakes hidden, which a search for "Bread" alone would also give. The no-match step does show the two combining | Accepted. Not weakened by this change |

## Verified
- The reviewer re-ran TDD in a separate worktree. With the attribute removed, the new test fails with `Tab keeps focus inside #catalog-category-filter-dialog — Expected: true, Received: false`. With it restored, the test passes.
- The 2119, 2097 and popup-scrim-2873 specs (`--project=default`) passed 40/40, in both the Dev and the review runs.
- The reviewer checked these by probe in the real app (Chromium):
  - opening focuses the close button (`UT.dialogOpening` covers it);
  - the close button, scrim tap and Escape all return focus to the trigger with `aria-expanded=false`;
  - the popover has no text input, so the on-screen keyboard and barcode wedge are unaffected.
- Help (`web/help/en/catalog.md`, `inventory.md`) says the filter "combines with the search box". That is still true, so the manual needs no change.
- Also run:
  - `gofmt -l .` (no output), `go build ./...`, `go vet`, `go test ./...`;
  - the CI `build` job's guards.

## Not checked
- Nothing visual changed, so no screenshots were taken and docs-shots were not regenerated.
- Not checked on real touch hardware. The change is keyboard-only, and pointer behaviour is unchanged because the scrim already covered everything.

## Verdict
Safe to merge.
