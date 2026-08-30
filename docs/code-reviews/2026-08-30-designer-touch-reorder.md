# Code review: Designer tile reorder has no touch path (ut-docs#1221)

**Date:** 2026-08-30
**Branch:** `fix/1221-designer-touch-reorder`
**Reviewer:** independent Opus subagent, worktree-isolated, fresh context
(model routing per `complexity:medium`)
**Card:** universaltill/ut-docs#1221

## What shipped

The Designer's quick-sale tile reorder was HTML5 drag-and-drop
(`draggable="true"` + `dragstart`/`dragover`/`drop`). WebKitGTK — and
browsers generally — never synthesize those events from touch input, so on
the till's actual touchscreen the whole reorder feature was dead: mouse-only,
on the device the feature exists for.

The fix replaces the gesture with an explicit **move-up (▲) / move-down (▼)
`<button>` pair per tile** in `web/ui/partials/buttons_admin.html`. Button
activation is one mechanism that already works by tap, click and keyboard
with no gesture-detection code. Also in the diff:

- `web/public/app.css` — the `-webkit-user-drag: element` workaround added by
  ut-docs#1170 is removed (moot with no draggable element left);
  `.draggable-tile` → `.reorderable-tile`.
- `web/locales/{en,ar,fa,tr}.json` — `designer.reorder_hint` reworded, new
  `designer.move_up` / `designer.move_down` keys in all four locales.
- `web/help/{en,ar,fa,tr}/catalog.md` — a new step (the `catalog` topic is
  the one claiming the `/designer` route); `web/help/img/manifest.json`
  regenerated via `make docs-shots`.
- `ut-docs/reference/feature-catalogue.md` — the cross-repo feature summary's
  stale "Drag to reorder" line updated to match.
- `e2e/tests/designer-reorder-1221.spec.ts` (new) — four specs, including one
  driven from a `hasTouch` context and one reproducing the concurrent-request
  race described below.

**Backend is untouched.** `POST /api/buttons/reorder`
(`internal/pages/buttons_api.go`) already accepted a repeated form-encoded
`codes` field in display order; only the frontend changed (plus its own
stale comment, see finding 7 below).

## Design call: buttons instead of ADR-0054's pointer-drag pattern

Accepted, and it does **not** contradict ADR-0054. That ADR's Decision 2
scopes its `pointerdown`/`pointermove`/`pointerup` pattern to *continuous x/y
placement* and names this very file as the contrasting case: HTML5
`draggable` is "the wrong tool for continuous x/y placement vs. discrete
reorder". A one-dimensional discrete reorder is not a positional editor, so
the ADR's precedent isn't engaged. The card's own acceptance criteria
explicitly left the choice open ("pointer-based dragging OR explicit
move-left/move-right controls per tile — UX role's call"), and buttons are
the stronger choice here: no `touch-action` tuning needed to avoid eating the
page's pan-scroll gesture (the exact hazard ADR-0054's own editor still
carries), and a keyboard path comes for free, which the drag never had. No
superseding ADR needed.

## Independent review — findings, and what was fixed

The independent reviewer ran the full gate (`gofmt`, `go build`, `go vet`,
`go test ./internal/pages/... ./internal/ui/...`, every relevant CI guard),
drove the real app in Chromium at 1024×600 and 360px in en/ar/fa/tr (looked
at, not just asserted), confirmed RTL renders correctly, and **independently
re-derived the TDD claim** by reverting only `buttons_admin.html` and
re-running the new e2e spec (red, `.reorderable-tile` count 0, for the right
reason), then restoring the fix (green). Nine findings came back; three were
blocking.

### Fixed (blocking)

1. **Concurrent reorder POSTs could persist a stale order.**
   `persistOrder()` fired a fire-and-forget `fetch` per click, each carrying
   a full order snapshot, with nothing serializing them — and the UI
   deliberately encourages repeated taps to step a tile several places. The
   reviewer reproduced a real race (4/4 with one slow response, 2/2 on a
   fast burst): the DOM showed the correct final order, the server persisted
   whichever response happened to land last. **Fix:** every send is now
   chained onto one running promise (`pending = pending.then(...)`), so
   sends can never overtake each other on the wire — each snapshot is
   already the fully up-to-date DOM order at click time (the DOM moves are
   synchronous), so serializing delivery order is sufficient. Added a
   regression test (`rapid repeated moves persist the final order, not a
   stale one`) that delays the first of two rapid requests and asserts the
   server ends up agreeing with the DOM after reload — confirmed red on the
   pre-fix code (exact same failure mode: DOM right, persisted order
   wrong), green after.
2. **New touch targets were below the product's own floor.** `.btn-actions
   .btn`'s `min-height: 0` shrank the new ▲/▼ to ~37×33px with ~4px gaps —
   under this product's own 46px `.btn-touch` standard, on the card whose
   entire point is touch, with an unconfirmed destructive ✕ 4.3px away.
   **Fix:** scoped `.reorderable-tile .btn-actions .btn { min-height: 46px;
   min-width: 46px; }` plus a wider gap, applied only to this tile's action
   row so the rest of the app's deliberately-compact action rows (e.g. the
   sale-screen tender panel) are unaffected. Screenshots regenerated and
   re-inspected at both breakpoints.
3. **Keyboard focus dropped to `<body>` at the end of a move.**
   `refreshEdgeStates()` ran before `btn.focus()`, so once a tile reached an
   edge the just-pressed button was already `disabled` and `.focus()`
   silently no-opped, forcing the operator to Tab from the top of the page.
   **Fix:** when the pressed button just became disabled, focus moves to
   the tile's other move button instead — still on the tile the operator
   was steering, still keyboard-reachable.

### Fixed (minor/nit, folded into the same pass)

4. Stale CSS comment claiming `.btn-actions .btn` already covered the touch
   target — corrected alongside finding 2.
5. `aria-label` said "Move up"/"Move down" while the grid is 6 columns wide
   at the kiosk floor, so ▲ visually moves most tiles left, not up — and the
   manual prose already correctly said "earlier"/"later". Changed the four
   locale *values* to "Move earlier"/"Move later" and their equivalents;
   keys unchanged, so no `lang-pack-drift` follow-up is triggered. The ▲/▼
   glyph choice itself was confirmed RTL-correct and is kept.
7. `internal/pages/buttons_api.go`'s handler comment still said "Drag&drop
   reorder" — reworded; the endpoint contract itself needed no change.

### Deferred / accepted as documented

6. `ut-docs/reference/feature-catalogue.md`'s stale "Drag to reorder" —
   fixed in this same PR (cross-repo doc edit, done in the same session per
   the standing "behaviour change updates the affected reference" rule).
8. Icon-only ▲/▼ glyphs are new to this UI (not used elsewhere in
   `web/ui/**`); verified they render correctly in the driven Chromium run,
   but WebKitGTK (the till's real desktop shell) wasn't exercised. Low risk,
   accepted — an inline-SVG hardening pass can follow if it ever shows up as
   a real tofu-fallback report.
9. `/designer` is never actually screenshotted by the docs-shots harness —
   it only shoots each topic's `routes[0]`, and `catalog`'s is `/catalog`.
   Pre-existing, documented gap of the same class already accepted for
   `inventory`/`multitill`; not introduced by this change. The reviewer
   took and inspected `/designer` screenshots directly as a substitute.

## Verified beyond automated tests

- Full gate green after fixes: `gofmt -l .` clean, `go build ./...`,
  `go vet ./...`, `go test ./internal/pages/... ./internal/ui/...` (4
  packages), and every CI-blocking guard in `.github/workflows/ci.yml`'s
  `build` job.
- `make docs-shots` re-run after the CSS fix (touch-target sizing is
  visible); `guard-docs-shots.sh` and `guard-help-topics.sh` both green.
- `npx playwright test designer-reorder-1221` — 4/4 pass, including the
  `hasTouch`-context spec and the concurrency-race regression test.
- TDD claims re-verified twice: once by the independent reviewer (revert
  `buttons_admin.html` → red for the right reason → restore → green), and
  again by me for the new concurrency test specifically (revert just the
  fix, keep the new test → red, same failure mode the reviewer described →
  restore → green).
- Data hygiene: no real client/shop name, no secret-shaped literal.

**Honesty note (per the `ux` skill).** All touch verification here is
Playwright's synthetic `hasTouch` context, not real touch hardware, and the
till's WebKitGTK desktop shell was never exercised — only Chromium. This is a
materially weaker caveat than it would be for a drag-based fix (a `<button>`
firing `click` on `touchend` is unconditional browser behavior, not a gesture
the engine has to recognize), but it isn't zero — the original undersized
touch targets are exactly the class of thing emulation doesn't complain about
and a finger does.

## Verdict

**Safe to merge.** All three blocking findings are fixed and covered by a
new regression test each was missing; the minor/nit findings folded into the
same pass are fixed or explicitly deferred with reasoning above.
