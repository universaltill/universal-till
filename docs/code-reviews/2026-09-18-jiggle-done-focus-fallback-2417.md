# 2026-09-18 — Jiggle-mode Done focus fallback falls to `<body>` (ut-docs#2417)

## What shipped

`web/public/app.js`'s jiggle edit-mode `exit()` has a focus fallback: when
keyboard focus is inside the Done bar (`b`) at the moment it's about to be
`display:none`'d, it tries to move focus onto a real, visible tile instead
of letting it fall through to `<body>`.

The fallback query was unscoped (`g.querySelector('.btn-tile[data-code]')`),
so it always resolved to the first matching element in DOM order —
regardless of whether that element was actually visible. Two distinct
sources of hidden tiles precede the active category panel in the DOM:

1. `#buttons-grid-all` (the All tab's own dedicated grid, ut-docs#2294),
   which always renders first inside `#buttons-grid`.
2. **Every other category's own `.products-tab-panel`** — all of them exist
   in the DOM at all times; only the currently-active tab's panel is
   actually visible (Alpine `x-show`). Whichever panel happens to render
   before the active one in template order contributes hidden candidates
   too.

`.focus()` on a hidden element is a silent no-op, so with the Done button
(inside `b`) also about to be hidden, focus fell all the way to `<body>`.

**Fix**: scope the candidate query with two filters, both already
established conventions in this same file:
- `!inAllGrid(t)` — the same helper `tileFor`/`badgeFor` already use to
  exclude `#buttons-grid-all`.
- `t.getClientRects().length > 0` — the same visibility check
  `visibleCells()` already uses a few lines above.

Both filters are necessary; `inAllGrid` alone is not sufficient — see
"What the independent review found" below.

New regression test: `e2e/tests/sell-tile-jiggle-done-focus-2417.spec.ts`
— seeds one tile, switches off the default All tab, long-presses to enter
jiggle mode, tabs focus to the Done button, activates it via keyboard
(Enter), and asserts the resulting `document.activeElement` is a real,
visible `.btn-tile` inside `.products-tab-panel`, never `<body>` and never
inside `#buttons-grid-all`.

## What the independent review found

Independent review via a fresh-context Sonnet subagent (`complexity:easy`
→ Sonnet review per model routing), isolated in its own git worktree.

**The first draft of the fix (excluding only `#buttons-grid-all`) was
insufficient**, caught by this task's own TDD process before review even
started: running the new test against that draft failed with focus
landing on a *different* hidden tile (`data-name="Butter 250g"`, inside
`cat-panel-cat_food` — a demo-seeded category panel that happens to
precede "Uncategorized" in template order and was hidden because it
wasn't the active tab). That's what motivated adding the
`getClientRects().length > 0` check on top of `inAllGrid()`. The final
fix (both filters) was what went to review.

The reviewer independently re-verified the TDD claim rather than taking it
on faith: reverted `web/public/app.js` back to its pre-fix state (keeping
the new test), reran the test, confirmed it failed reproducing the exact
`<body>`-focus bug, then restored the fix and confirmed it passed again.
Also searched the file for other unscoped `.btn-tile[data-code]` queries
that might need the same fix (`orderedCodes()`, `refreshPositions()`) and
confirmed both already filter `inAllGrid` — no other caller needed
changing, and no other focus-fallback pattern exists in the file. Ran the
full `sell-tile-jiggle-mode-2339.spec.ts` suite alongside the new test — 4
passed, no regressions in long-press entry, drag reorder, badge
edit/remove, tap-outside exit, or the All-tab long-press guard (#2402).
Confirmed `gofmt -l .` clean and `go build ./...` succeeds (no `.go` files
touched). Confirmed test data is generic (no real shop/client name), no
secrets.

No blocking findings. Verdict: **safe to merge as-is**.

## What was verified beyond automated tests

- TDD revert/restore done twice independently: once by Dev while writing
  the test (which is what caught the first draft's insufficiency), once
  by the independent reviewer from a clean worktree.
- Confirmed via template source (`web/ui/partials/buttons.html`) that
  `#buttons-grid-all` (line 571) and the category-panel loop (line 612+)
  render in the order the bug description assumes.
- No i18n/data-access/compliance guard implicated — pure client-side JS
  focus-management logic, no new user-facing strings, no SQL, no money.
  `guard-i18n.sh` and `guard-data-access.sh` both pass (unaffected, but
  run anyway as part of the standing gate).
- No manual/help-topic update needed: this restores previously-intended
  (but broken) invisible focus behavior — no new screen, no changed step
  a shop owner would see documented differently.

## Deferred / out of scope

None. This is a contained, single-file logic fix plus its regression
test.

## Safe to merge

Yes.
