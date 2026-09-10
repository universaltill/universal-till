# 2026-09-10 — OSK reserved height tracks the keyboard's real height (ut-docs#1998)

## What shipped

`body.osk-padded`/`.payment-overlay`/`.item-form-modal` (`web/public/app.css`)
reserved a hardcoded `15.5rem` for the on-screen keyboard (`#osk`,
`web/public/osk.js`) — ~1.45rem short of the keyboard's real rendered height
(288px measured at this build's 17px root font-size). Nothing was actually
covered only because `.catalog-form-body`'s own bottom padding happened to
absorb almost exactly the shortfall — correct by accident, not by design —
so any future change to that padding, the root font-size, or the OSK's own
height would silently start covering the last control in a form.

Fix: `osk.js`'s `show()` measures `#osk`'s own `getBoundingClientRect().height`
and writes it to `--osk-reserved-height` on `<html>`; every CSS reservation
reads that custom property instead of a hardcoded number. Also re-measures
on a `?123`/`ABC` layer switch (not just once per `show()`) and on
resize/rotation while the keyboard is open, each write skipped internally
when the height hasn't actually changed.

New e2e spec `e2e/tests/osk-reserved-height-1998.spec.ts` pins: the reserved
custom property equals the OSK's real height; the CSS actually **consumes**
that property (asserts the applied computed `padding-bottom`, not just the
property's existence); and the last control in the item form stays a real,
un-covered hit target at 1024×600 and 360px.

## What the independent review found

Opus subagent (complexity:medium → Opus review per `MODEL-ROUTING.md`),
isolated worktree, ~55 min. **Verdict: PASS, no blockers.**

- Independently confirmed the central claim with the box model, not just
  the ticket's own number: `#osk` = 5 rows × 2.9rem + 5 × 0.3rem margin +
  0.9rem padding = 16.9rem, which at 17px root font-size = 288.234375px —
  matching "288px" exactly, and confirming `15.5rem` (263.5px) was ~1.4rem
  short. Also measured root font-size as 17.75px at 1280×720 (OSK =
  300.875px) — the strongest argument for measuring over re-hardcoding: the
  constant genuinely isn't one number.
- **Should-fix, applied:** the CSS half of the fix (the three rules actually
  reading `--osk-reserved-height`) was not pinned by any test — reverting
  *only* `app.css` back to the hardcoded `15.5rem`, while keeping the fixed
  `osk.js`, still passed all 3 original tests. Root cause: at some viewports
  the pre-existing shortfall is small enough to still be absorbed by
  `.catalog-form-body`'s own incidental padding — the exact "correct by
  accident" failure mode this card was filed about, so the indirect
  hit-target check alone couldn't catch a CSS-only regression. Fixed by
  adding a direct assertion on `getComputedStyle(document.body).paddingBottom`
  against the OSK's real height. Re-verified: reverting only `app.css` now
  fails this assertion (`Expected: 300.875, Received: 275.125`); restoring
  passes again.
- Nit, applied: two stale comments (`app.css` lines ~955, ~1484) still cited
  the deleted `15.5rem` constant — corrected to reference
  `--osk-reserved-height`.
- Nit, applied: the `?123`/`ABC` layer switch called `render()` but not
  `updateReservedHeight()` — benign today (every layer renders exactly 5
  rows, verified by reading `LAYOUTS`), but the invariant was unenforced.
  Now re-measures after every layer switch, self-maintaining against a
  future layer gaining/losing a row; a `lastReservedHeight` guard inside
  `updateReservedHeight()` keeps this (and the existing `resize` listener)
  a cheap no-op whenever the height hasn't actually changed.
- Confirmed no measurement race: no `transition`/`animation` applies to
  `#osk`/`.osk-row`/`.osk-key`; `display: none → block` isn't animatable;
  row height is pinned by `.osk-key`'s `min-block-size` well above the
  tallest key's line box, so a font swap can't affect it. Empirically
  stable across ~24 runs.
- Confirmed "every layout renders exactly 5 rows" is true for all of
  `en`/`tr`/`fa`/`ar`/`de`/`es`/`sym`/`num`/`numSigned`, by reading `LAYOUTS`
  directly, not by trusting the comment.
- Confirmed the `resize` listener correctly no-ops when closed, and is
  never registered at all when OSK mode is `off`.
- Confirmed RTL has no implication (`padding-block-end`/`block-size` are
  logical; `height`/`max-height` are block-axis only, unaffected by `dir`).
- Confirmed field-switch staleness is handled correctly: `show()`'s early
  return only fires on re-tapping the *same already-open* field; switching
  between two different OSK-able fields falls through the full
  `render()` → `osk-open` → `updateReservedHeight()` path.
- Confirmed the two recurring bug classes (missing `os.MkdirAll`, a
  cwd-relative path instead of `paths.Data(...)`) are correctly out of
  scope — no file writes anywhere in the diff.
- No new user-facing strings; no client/shop names or secret-shaped
  literals; no `web/help/` topic update needed (invisible layout
  correction, no operator-visible step/wording/route change) — only the
  screenshot regen the pixel shift itself requires.

## TDD re-verification

By the reviewer, independently: reverted `osk.js` to `origin/main` (0
occurrences of `updateReservedHeight` on disk) — the reserved-height test
**genuinely failed twice** (`Expected: 300.875, Received: NaN`); restored,
re-ran 18/18 via `--repeat-each=6`.

Separately, after applying the review's should-fix (the CSS-consumption
assertion): reverted *only* `app.css` — the same test **genuinely failed**
on the new assertion (`Expected: 300.875, Received: 275.125`, i.e. the
hardcoded 15.5rem at that viewport's root font-size); restored, passes
again. This closes the exact gap the review found.

## Verified beyond automated tests

- Reviewer's own runs: the 6 originally-requested suites (46/46), the new
  spec at `--repeat-each=6` (18/18), 8 additional OSK-layout-sensitive
  specs (32/32), and the **full e2e suite (409/409)**.
- Post-review-fixes re-run of the same 6 suites: 46/46.
- `go build ./...`, `gofmt -l .` clean.
- `scripts/ci/guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-e2e-fixtures-import.sh`, `guard-docs-shots.sh` all clean.
- `make docs-shots` regenerated (the ~25px reservation increase shifted
  layout on 7 topics × up to 4 locales) and the guard re-confirmed fresh.

## Safe to merge

Yes — independent review PASS, no blockers. The one should-fix and two
nits were applied and independently re-verified (including a second,
targeted TDD revert proving the should-fix's own value).

## Explicitly deferred / could not verify

- Real-device behaviour on kiosk hardware (WebKitGTK/Wayland) and Android
  WebView — specifically `env(safe-area-inset-bottom)` actually being
  non-zero, genuine rotation firing `resize`, and a real `ui-scale`
  change. Chromium at fixed viewports cannot exercise any of these; the
  `resize` listener exists specifically for this class of device event.
- No pixel-level screenshot diffing beyond the regenerated manual
  screenshots + a manual look (per `ux`'s "screenshot exists and was
  looked at" gate) — the geometry assertions are the load-bearing check.
