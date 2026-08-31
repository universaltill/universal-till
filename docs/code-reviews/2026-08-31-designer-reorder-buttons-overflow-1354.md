# Code review — Designer reorder buttons overflow their tile (ut-docs#1354)

- **Date:** 2026-08-31
- **Branch:** `fix/1354-designer-reorder-buttons-overflow`
- **Reviewer:** independent fresh-context Sonnet subagent (complexity:easy —
  fresh-context-model review per the model-routing rule's easy-tier
  exception), full findings below
- **Verdict:** Safe to merge.

## Context

ut-docs#1221 replaced the Designer's HTML5-drag-and-drop tile reorder with
three per-tile buttons (▲ move-up, ▼ move-down, ✕ remove), each held to a
46px touch-target floor (`.reorderable-tile .btn-actions .btn`,
`app.css:418-419`). `#buttons-grid-admin` (the Designer's tile grid) shares
the `.grid` class with the sale-screen product grid, whose column-width
floor was independently narrowed to `minmax(7rem, 1fr)` for sale-screen
density (2026-08-30, `app.css:1034`'s own comment). At 7rem a Designer tile
doesn't have enough content width for the three 46px buttons + two `.5rem`
gaps (~154px needed), so `.btn-actions` — a plain `display:flex` row with
no wrap — overflows the tile's right edge and visibly bleeds into the
neighboring tile. Live-verified by the product owner via adb screencap
against a real tablet (v0.8.0).

## What this change does

- `web/public/app.css`: adds `#buttons-grid-admin { grid-template-columns:
  repeat(auto-fill, minmax(11rem, 1fr)); }` right after the shared `.grid`
  rule. Scoped by ID so only the Designer admin grid's column floor widens
  — the shared `.grid` class, and the sale-screen product grid that also
  uses it, are untouched.
- `e2e/tests/designer-reorder-buttons-overflow-1354.spec.ts` (new):
  regression test at the till's actual kiosk viewport (1024x600) — the
  width at which the grid's `auto-fill` actually drives multiple columns
  down to the floor (a narrow phone viewport has too few columns for the
  floor to ever bind, so it would never reproduce there). Measures the
  rightmost **direct child** of `.btn-actions` (not `.btn-actions`'s own
  box, which is a no-wrap flex container that doesn't grow to contain
  overflowing children) against (a) its own tile's right edge and (b) the
  next tile's left edge when one exists on the same row.

## Review notes (independent subagent's own findings)

- **TDD re-verified independently, not trusted from my own claim**: CSS
  hunk reverted via `git stash`, test file left in place — test FAILED
  (`tile 0's reorder buttons overflowed its own tile's right edge`,
  received 236.25 vs expected ≤220.75 — a real 15.5px geometry failure, not
  a timeout/selector miss). Fix restored — test PASSED, re-run 3× total
  with no flake. Existing `designer-reorder-1221.spec.ts` suite (4 tests)
  unaffected.
- **Selector scope**: `#buttons-grid-admin` is a unique ID used exactly
  once in the codebase; no other `.grid` user collides, no `@media` block
  in `app.css` touches `.grid` or this ID, so no breakpoint reintroduces
  the narrow floor.
- **11rem arithmetic independently re-derived**: with global
  `box-sizing: border-box`, `.btn-tile`'s asymmetric
  border-inline-start/padding-inline-start override works out to 7.4px of
  horizontal chrome on each side (14.8px total). At 176px (11rem), content
  width = 161.2px against a 154px need — a real but tight ~7.2px margin.
  Not a blocking concern: the new e2e test measures actual rendered pixels
  at runtime, so it will re-fail if any future change to button
  padding/gap erodes that margin, rather than silently drifting.
- **Acceptance criteria** (from the ticket) all met: no overflow at the
  grid's real minimum tile width (verified empirically); the 46px
  touch-target floor is untouched (only `grid-template-columns` changed);
  regression test is at the real kiosk viewport, not a wide desktop one.
- **Designer reorder JS** (`buttons_admin.html`'s inline script) walks tiles
  via `previousElementSibling`/`nextElementSibling` and `data-code` — no
  column-count or row-width assumption, so fewer/wider columns can't break
  it.
- **Test construction**: uses `tileCount > 1` (not a hardcoded exact count,
  robust to demo-catalog changes); correctly skips the "next tile" check
  when there's no next tile on the same row; `+1` epsilon only absorbs
  subpixel rounding, not the actual overflow class of bug.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | clean |
| `go vet ./...` | clean |
| `go build ./...` | clean |
| `go test ./...` (full repo) | all pass |
| `guard-data-access.sh` | pass |
| `guard-i18n.sh` | pass (no new user-facing strings) |
| `guard-compliance-claims.sh` | pass |
| `guard-help-topics.sh` | pass (no new page route) |
| TDD: new e2e spec | fails pre-fix (real 15.5px overflow), passes post-fix, re-run 3x clean |
| Existing `designer-reorder-1221.spec.ts` | 4/4 pass, no regression |

No help-manual update needed — pure layout fix to an existing admin page,
no new page/behavior a shop owner would need instructions for.

## What this does NOT do

Doesn't touch the sale-screen product grid's own 7rem density floor (a
deliberate, unrelated, product-owner-driven choice) — only the Designer's
admin grid widens.
