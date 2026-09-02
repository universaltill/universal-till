# Code review: category-switch stale-tile-add regression coverage (ut-docs#1433)

- **Date**: 2026-09-02
- **Branch**: `fix/1433-category-switch-stale-tile-add`
- **Card**: universaltill/ut-docs#1433 — "Sale screen: tapping a product
  tile shortly after switching category added the PREVIOUS category's item
  at that position (seen once on the tablet, wrong item in the basket)"
- **Diff**: `e2e/tests/category-switch-stale-tile-add-1433.spec.ts` (new,
  181 lines). No production code touched.

## What this change is

A one-off, real-tablet bug report with detailed repro steps but a single
observed occurrence. Following the card's own acceptance criteria ("make
it fail first, then fix"), the Dev step wrote a faithful Playwright repro
before attempting any fix. It could not be made to fail — three variants
(fast tap, slow ~5s tap matching the original timing, and a real
`touchscreen.tap()` dispatch closer to how `adb shell input tap` drives an
Android WebView) all pass, every time, on this codebase's current `main`.

## Root-cause investigation performed (by the Dev subagent, re-verified here)

Every candidate mechanism the card itself named was checked against actual
code, not just reasoned about:

- **Duplicate barcode/data collision** — `internal/data/shortcuts_repo.go`
  inserts shortcuts with `ON CONFLICT(barcode) DO UPDATE`, so two tiles can
  never carry the same `code`; structurally rules out a stale-code
  collision on the data side.
- **`hx-sync="#basket:replace"` targeting the wrong element** — read the
  vendored htmx source; the sync target is re-resolved fresh per request
  against the live `#basket` node, which is wholesale-replaced
  (`outerHTML`) after every scan. No mechanism for a 5-8-second-old
  reference to survive.
- **Server-side "last active category" state** — `/api/pos/scan` resolves
  strictly from the POST body's `code`; no session/category state in that
  path (`internal/pages/pos_api.go`, `internal/ui/buttons.go`).
- **CSS overlap / stale hit-testing** — `x-show` toggles a real inline
  `display:none`; a hidden panel is removed from hit-testing entirely.
  Verified directly: the test asserts category B's tile renders at the
  exact same `x`/`y` category A's did (the report's own "same position"
  premise), and a coordinate tap there still resolves correctly.
  Categories are not list-diffed — every tile is a distinct DOM node
  emitted once at page load (`buttons.html`); a tab switch never recycles
  nodes, ruling out the classic "reused the wrong DOM node" bug class.
- **A click/touch delegation caching a stale element** — audited
  `web/public/app.js`'s pointer listeners; none touch click targeting.

## Verification performed independently in this review (not just trusting the Dev report)

- `gofmt -l .` — clean.
- `go build ./...`, `go test ./...` — all green, no failures.
- `cd e2e && npx playwright test category-switch-stale-tile-add-1433
  --project=default` — **re-ran myself**: 3/3 pass (fast tap 3.0s, slow tap
  7.5s, real-touch slow tap 7.5s).

## Decision: no fix ships, the regression test does

The card's own acceptance criteria required reproducing the bug before
fixing it. It doesn't reproduce, and the audit above leaves no plausible
live mechanism in this codebase's current `main` to fix. Shipping a
speculative "fix" for an unconfirmed defect would be worse than shipping
nothing — it would touch working code on the strength of a guess. The test
itself is genuine, durable value regardless: it encodes the exact reported
scenario (same grid position, both ends of the reported timing window,
real touch dispatch) and will catch a future regression of this shape
even though it isn't currently red.

**Most likely explanation for the original report**, not confirmed: a
one-off Android WebView touch-dispatch quirk or a rare Alpine.js
reactivity anomaly, neither reproducible from this environment. The same
session's own later attempt (different item pair, same tab-then-wait-tap
pattern) worked correctly, which is consistent with a rare, non-deterministic
one-off rather than a standing defect.

## Outcome on the board

Card closed with this finding recorded on the issue (see the closing
comment) rather than left open indefinitely for a defect nothing can
currently reproduce or fix. Re-opens on the strength of a second,
better-evidenced occurrence (ideally with browser/devtools console output
or a screen recording from the device itself, which the original report
did not have).
