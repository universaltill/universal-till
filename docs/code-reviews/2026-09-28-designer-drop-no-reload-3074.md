# Code review — Designer: category/tile drop and Move earlier/later stay in place (ut-docs#3074)

- **Date:** 2026-09-28
- **Card:** universaltill/ut-docs#3074 (p1, complexity:medium, lane:cloud-54)
- **Author:** Opus 5.5 (dev subagent) · **Reviewer:** Fable (independent, isolated worktree)
- **Verdict:** safe to merge (two minor findings fixed before commit)

## What shipped

The product owner reported that dropping a category in the quick-button
Designer (sell screen → pen) reloaded the replica and left the page
somewhere else. Two causes were measured:

1. A successful reorder fired `buttons-changed` (`data-reorder-refresh`),
   which re-fetched `/ui/buttons` and outerHTML-swapped the whole `.products`
   root (strip, grid and list). Move earlier/later did the same through its
   own `htmx.ajax` POST, then re-opened the row form with an Alpine
   `x-data` seeding hack.
2. Chrome scroll anchoring scrolled the page after a moved on-screen row
   (852 → 952 px on Move later), even without a re-render.

Changes:
- `web/public/list-reorder.js`: bubbling `list-reorder:saved` (detail.ids)
  after an accepted save; `refreshAfterSave`'s `htmx:afterSettle` listener
  no longer leaks when the refresh never settles (5 s cleanup) — #2713.
- `web/ui/pages/designer.html`: Move earlier/later go through
  `utListReorder.move()` (form stays open, focus stays); on
  `list-reorder:saved` the strip tabs, overflow tiles, tab panels and
  category-group sections are permuted in place and the strip's
  `applyCategoryOverflow` re-runs; Move-button disabled state is kept in
  sync after a move, save and revert, with focus kept on the twin when the
  focused button becomes disabled. The htmx.ajax / re-open code is gone.
- `web/ui/partials/buttons.html`: the Designer list no longer carries
  `data-reorder-refresh`; `data-tab-id` / `data-cat-group` permutation keys.
- `web/public/app.css`: `.designer-cat-list { overflow-anchor: none; }`.
- Tile drop was already in place (`persistOrder` → `refreshPositions`);
  now covered by e2e.
- Help topic `till-designer` step 6 updated in en/de/fa/ar/tr; manifest
  regenerated with `make docs-shots`.

## Tests

- New `e2e/tests/designer-drop-no-reload-3074.spec.ts` (5 tests): category
  drag, Move later/earlier (keyboard + mouse), tile drag + Done, last-row
  edge, refused save (409 → revert). Each asserts every scroll offset is
  unchanged, no main-frame navigation, zero `GET /ui/buttons`, the same
  `.products` node, strip order, and the order after reload.
- `e2e/tests/sale-only-cashier-3079.spec.ts` (auth project) extended: a
  cashier sees no pen/add link, `/designer` → 403,
  `/api/designer/categories/reorder` → 403, `/api/buttons/reorder` →
  elevation prompt.
- `internal/pages/category_list_drag_2699_test.go` now asserts the Designer
  list has no `data-reorder-refresh`.

**TDD re-verified by the reviewer** in an isolated worktree: with the four
non-test files reverted to `origin/main`, the new spec failed 3/5
(root re-rendered; Move order raced to `[b,a,c]`; refused move scrolled
852 → 884); restored → 5/5.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | ar/tr help step 6 missing the new "moves in place, no reload" sentence (structure guard can't see prose drift) | **Fixed** — sentence added, `make docs-shots` re-run |
| 2 | minor | a save/revert that disables the focused Move button dropped focus to `<body>` (only the click path had the twin fallback) | **Fixed** — shared `keepMoveFocus` used by click, saved and reverted |
| 3 | nit | review record missing | **Fixed** — this file |
| 4 | nit | a legitimate `buttons-changed` landing while a reorder POST is in flight can render the old order until the next refresh (pre-existing; same window as before) | accepted |
| 5 | nit | e2e scroll-state keys include `className` — brittle if a scrolled element toggles a class | accepted |

Reviewer also verified: `permute()` moves each named node exactly once;
categories missing from the strip (inactive/uncategorised) keep their slots;
Alpine treats the synchronous batch as moves; page-scoped body listeners are
dropped by base.html on page swap; a detached list's saved event cannot
reach body; revert goes to the last accepted order the strip already shows;
cashier gating at `/designer`, the reorder route, the pen, and
`/api/buttons/reorder`. No new user-facing strings; the CSS is
direction-neutral; no file writes.

## Gate

Dev: `gofmt -l .` clean, `go build`, `go vet`, `go test ./...` (73 pkgs ok),
`golangci-lint` 0 issues, `shellcheck scripts/ci/*.sh` 0 issues, every
ci.yml `build` guard; e2e related default specs 59/59 and auth 29/29.
After the review fixes: docs-shots regenerated; designer/list-drag/
wysiwyg/jiggle/narrow specs 25/25; guard-i18n, docs-shots, help-topics,
help-drift, kiosk-engine, e2e-no-browser, compliance-claims,
competitor-naming all pass.

Visual: 1024×600 light-theme screenshot after an in-place Move looked at
(form open, focus ring on Move later). Not checked: dark theme, RTL,
German at 360 px — the only CSS change is `overflow-anchor`, no layout
effect.

## Deferred

- The refusal reason in `#designer-categories-msg` can render above the
  viewport when the list is scrolled (pushes rows ~31 px; scrollTop itself
  unchanged) → follow-up card.
- Owner rule "controls hidden for cashier" vs #2312's sale-screen jiggle +
  manager-PIN elevation for a cashier — needs a product decision; noted on
  the card.
- `data-reorder-refresh` support and the reorder route's
  `HX-Trigger: buttons-changed` are now unused by the Designer; tidy later
  (#2713).
