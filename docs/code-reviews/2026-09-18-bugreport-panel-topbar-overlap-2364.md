# Code review — bug-report panel head bar hidden under phone-width top bar (ut-docs#2364)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2364 (`complexity:easy`)
- **Branch:** `fix/2364-bugreport-panel-topbar-overlap`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing — a clean instance that never saw the
  dev reasoning, `MODEL-ROUTING.md`'s "different model relaxes to
  different instance" rule for easy cards).
- **Verdict: SAFE TO MERGE AS-IS.** No blocking, should-fix, or nit
  findings.

## The bug

Below 480px width, `.nav` reverts from the fixed-height desktop rail to
a horizontal top-bar fallback that wraps to a variable number of rows
depending on content/locale — currently 3 rows (~12rem) at 360px.
`.bugreport-panel`'s resting position at that width was a hardcoded
`inset-block-start: 4.6rem` (`web/public/app.css`), left over from when
the bar only wrapped to 2 rows. The panel's own head bar (title,
Discard, ✕) opened hidden underneath the top bar, reachable only by
scrolling the panel itself.

## What shipped

- `web/public/app.css`: a new `--topbar-h` custom property
  (`:root`, fallback `12rem` — a safe pre-JS guess, erring toward
  over-reserving). `.bugreport-panel`'s `<=480px` rule changed from
  `inset-block-start: 4.6rem` to `inset-block-start: calc(var(--topbar-h)
  + .5rem)`.
- `web/public/app.js`: a new IIFE measuring `.nav`'s real
  `getBoundingClientRect().height` under a `matchMedia('(max-width:
  480px)')` guard, writing it to `--topbar-h` via
  `documentElement.style.setProperty`, guarded against redundant writes,
  called once on load and again on resize — mirrors the pre-existing
  `--osk-reserved-height` pattern in `web/public/osk.js`'s
  `updateReservedHeight()` exactly (same shape, same guard style).
- `e2e/tests/bugreport-panel.spec.ts`: two new tests at 360×640 and
  360×740 asserting the panel's `.bugreport-head` box has no vertical
  overlap with `.nav`'s box, and that the Discard/close controls are
  actually in-viewport and clickable (not just a coordinate check).
- `web/help/img/**` + `manifest.json`: `make docs-shots` regenerated —
  `app.css`/`app.js` are hashed page-surface inputs for every topic, so
  any change to either invalidates freshness for all 31 topics × 4
  locales. Spot-checked `en/sell.png` (desktop rail, >480px) visually —
  unaffected, as expected for a change scoped to the `<=480px` media
  query.

## What the independent review found

**TDD claim re-verified independently, done for real**: reverted the
CSS fix back to the original `inset-block-start: 4.6rem`, re-ran the two
new tests — both failed red on the real regression
(`Expected: >= 200.625, Received: 93.625` at 360×740, i.e. the head bar's
top edge sits ~107px inside the top bar's own box). Restored the fix,
diffed back to the committed version — byte-identical. Full
`bugreport-panel.spec.ts` suite: 17/17 passed with the fix in place,
including every pre-existing drag/viewport test at 1280×720, 1024×600
and 1280×800 — no regression to any of those.

**JS measurement guard verified correct**: `updateTopbarHeight()` returns
early unless `mq.matches` (`<=480px`), so it never measures/overwrites
`--topbar-h` from the full-height desktop rail. On a resize from wide to
narrow, the listener re-fires and measures the real (now-mobile) `.nav`
height correctly. `--topbar-h` is also only ever consumed inside the
CSS's own `<=480px` block, so even a stale value at a wider width would
be inert.

**Drag math independently confirmed unaffected**: read
`web/ui/partials/bugreport_panel.html`'s `moveTo`/`clampIntoViewport`/
`reclampIfDragged` — dragging switches the panel to explicit
`panel.style.left/top` pixel offsets, entirely bypassing
`inset-block-start`; `reclampIfDragged()` only acts on a panel that was
actually dragged (`if (panel.style.left)`). The undragged resting
position is 100% CSS-driven, so this change only touches that path.

**RTL confirmed clean**: only `inset-block-start` (already a logical
property, already in use) was touched — no `left`/`right` literal
introduced. Manually verified live at 360×640 with `?lang=ar`: the panel
mirrors to the correct (inline-start) side and its head bar sits fully
below the 3-row Arabic-labelled top bar with no overlap.

**Kiosk floor (1024×600) manually verified**: unaffected, as expected —
stays in the `>480px` desktop-rail regime, panel opens at its normal
`inset-block-start: 1rem` position.

Ran the full gate: `gofmt -l .`, `go build ./...`, `go test ./...`
(whole repo, all green), `golangci-lint run ./...` (0 issues — no `.go`
files touched by this change, as expected for a CSS/JS/test-only fix).
Also ran `guard-i18n.sh`, `guard-e2e-fixtures-import.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh` (pre-existing baselined
drift only, unrelated to this change), `guard-compliance-claims.sh`, and
`guard-docs-shots.sh` (green after the regeneration above) — all clean.

No i18n/money/repository-pattern/offline-first concerns apply: no new
user-facing strings, no SQL, no monetary values, no network-dependent
behavior touched.

## Non-blocking nits

None found.

## Explicitly deferred

Nothing — this card's stated acceptance criteria (head bar fully visible
below the top bar at 360px, no overlap at 360×640/360×740, logical
properties only) are fully met with no follow-up gaps found.
