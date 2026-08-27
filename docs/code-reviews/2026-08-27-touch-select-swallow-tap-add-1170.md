# Code review: touch tap swallowed as text-selection on `.btn`/`.btn-tile` (ut-docs#1170)

**Date:** 2026-08-27
**Author (pipeline lane):** `lane:cloud-54`, Sonnet dev (inline) + Sonnet
tester, independent Opus review subagent (worktree-isolated)
**Card:** universaltill/ut-docs#1170

## What shipped

`web/public/app.css`'s `.btn` and `.btn-tile` base rules gain
**unconditional** `user-select: none; touch-action: manipulation;`. The
only place these properties existed before was gated behind `body.kiosk`
— which ut-docs#1021's own confirmed hardware investigation showed never
applies on a windowed desktop-shell till (`unitill-kiosk.service`
inactive, `display.window_mode=normal`). That is the actual mechanical
cause of the operator-reported bug: a tap on the Designer's search-result
"add" button (`web/ui/partials/buttons_admin.html`, `class="btn inline
result"`) could be swallowed as a text-selection drag instead of
dispatching its click, so `hx-post="/api/buttons/add"` silently never
fired.

Two new/changed e2e specs:
- `e2e/tests/designer-search.spec.ts` — a computed-style assertion (the
  actual TDD proof: `userSelect`/`touchAction` on the `.result` button,
  asserted outside kiosk mode) plus a touch-context behavioral tap-to-add
  test, kept as flow documentation only (see Honesty notes below).
- `e2e/tests/tables-touch-drag-1170.spec.ts` (new) — confirms the
  pre-existing floor-plan drag-to-move (ADR-0054/#814, untouched by this
  change) still works via a synthetic touch Pointer Event sequence; zero
  prior e2e coverage of that gesture existed.

`web/help/img/manifest.json` regenerated via `make docs-shots` (twice —
see Review finding below); screenshots are byte-identical both times,
only the surface hash moved.

## Independent review (Opus, worktree-isolated, fresh context)

Re-derived the TDD claim personally rather than trusting the write-up:
reverted exactly the two added lines, confirmed the computed-style test
fails with `Expected: "none", Received: "auto"`, restored, confirmed
green again. Ran the full gate itself (gofmt, go build, go vet, go test,
all 29 CI-blocking guards, the full e2e suite — 204/204 at that point) —
all clean, independently, not just re-stated from Dev/Tester.

**Real finding, fixed:** `.btn-tile` is *also* the class on the
Designer's HTML5-`draggable` reorder tile
(`buttons_admin.html`: `<div class="btn-tile draggable-tile"
draggable="true" ...>`). WebKitGTK — the Linux desktop shell
(`cmd/unitill-desktop`, ADR-0028, `guard-webkit-version.sh`; *not* the
Chromium kiosk shell, which is unaffected) — has a known behavior where
`user-select: none` on a `draggable="true"` element can suppress HTML5
drag-and-drop initiation. This diff newly applies `user-select: none` to
that tile on exactly the engine where the interaction is known to be
risky, on the very page under fix, with **zero** existing e2e coverage of
the mouse-driven reorder gesture (`internal/pages/buttons_reorder_test.go`
only tests the POST endpoint, not `dragstart`/`drop`). The reviewer could
not test on real WebKitGTK (no WebKit build in the sandbox) but flagged
it as a credible, well-reasoned risk with a cheap, standard, Blink-inert
fix.

**Fix applied:** `.draggable-tile { -webkit-user-drag: element; }` —
restores WebKit's own drag-and-drop initiation on that element
regardless of `user-select`, no-op on Chromium/kiosk. Re-ran the full
gate after this fix: gofmt/build/vet/test clean, all guards pass
(`guard-docs-shots` required a second `make docs-shots` regen — the new
rule touches `web/public/app.css` again, screenshots stayed
byte-identical), full e2e suite re-run: **203/204** (see below).

**Counter-example hunt (nothing else found):** searched for `.btn` on
`input`/`textarea`/`select` (none), any selection-based copy mechanism
(`getSelection`/`execCommand`/clipboard APIs — none in `web/ui`+
`web/public`), `a.btn` wrapping a copyable `mailto:`/`tel:` address
(none). The app's one genuinely copyable secret — the replica pairing
code (`internal/pages/sync_api.go:277`) — is `<code
style="user-select:all">` inside a plain `<div>`, not a `.btn`; its own
inline style outranks the class rule regardless, so it's unaffected
either way. `touch-action: manipulation` computes to `pan-x pan-y
pinch-zoom` — it cannot break scrolling (only suppresses double-tap-zoom);
`touch-action: none` would have been the dangerous choice and isn't what
shipped.

**Nits, accepted as out of scope for this diff (not fixed here):**
- `deactivateAllTables`/`createTableViaCard` are now duplicated a third
  time across three `tables-*.spec.ts` files; two of the three copies
  predate this change. Belongs in `e2e/tests/helpers.ts` — cosmetic,
  test-only, not filing a separate Backlog card for it (low enough value
  that tracking it costs more than it saves; the next touch to any of
  those three files is a natural moment to fix it).
- `designer-search.spec.ts`'s new touch-context test closes its own
  browser context after `assertClean()`, so a context leaks on assertion
  failure. Cosmetic, no functional impact (the test run's own teardown
  reclaims it).
- `e2e/tests-docs/lib.js`'s `algorithm` string (baked into
  `manifest.json`) undersells its own fileset — it omits `web/public/`
  from its description even though `surfaceFiles()` walks it, which is
  exactly why this diff's manifest bump could look spurious at a glance.
  Pre-existing, not introduced here — worth a Backlog card, filed
  separately (see Close-out).

No money/tax logic, no plugin-verification surface, no file writes (so
the `os.MkdirAll`/`paths.Data(...)` bug classes don't apply — no Go
files touched at all), no secrets, no real client/shop name.

## Re-verification after the review fix (this pass)

- `gofmt -l .` clean, `go build ./...` clean, `go vet ./...` clean,
  `go test ./...` — all packages pass.
- All CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list pass, including `guard-docs-shots` after the second
  `make docs-shots` regen (surface hash `1318f99b0a7b…`).
- Full `e2e/` Playwright suite (not just the touched specs — this is an
  app-wide CSS change): **203 passed, 1 failed.** The one failure
  (`catalog-image-to-till.spec.ts` — an image-load timing assertion) is
  confirmed **pre-existing and unrelated**: reproduces identically
  against unmodified `HEAD~2` (before this card's commit), unrelated to
  buttons/touch/drag entirely (image `naturalWidth`/`complete` timing).
  Not a regression from this change.

## Honesty notes (carried from the commit message, independently confirmed)

- The touch-context tap-to-add behavioral test in `designer-search.spec.ts`
  passes even against the **unfixed** CSS — Playwright's synthetic
  `locator.tap()` on a `<button>` doesn't reproduce WebKitGTK's real
  selection-swallow bug. It's kept as flow documentation, not proof; the
  computed-style test is the one that actually goes red→green and is the
  real evidence for this fix.
- The table-drag confirmation test passes both pre- and post-fix, as
  expected for a test confirming already-working, pre-existing code (not
  a TDD-red test for this change) — verified during Tester's pass by
  deliberately disabling the drag handler and confirming the test then
  fails hard (30s timeout on the position POST), so it's a real
  regression-catcher, not a tautology.
- The general cross-page touch-scroll story (ut-docs#1021) stays
  untouched and `blocked:env`, pending real Pi + DevTools reproduction —
  not re-attempted here.

## Not verified here (accepted, tracked)

- Real WebKitGTK hardware behavior for either the fixed bug or the
  `-webkit-user-drag` mitigation — the sandbox has Chromium only. Both
  are reasoned from documented engine behavior and cheap/inert if wrong,
  not from an observed pass on the real desktop shell.
- RTL/`fa` visual screenshot of `/designer` specifically (a manual
  verification script's locale override didn't take — locale is
  server-session-driven, not browser-context-driven); the existing e2e
  suite's own RTL specs did pass, and this CSS change has no
  layout/paint effect by nature (`user-select`/`touch-action` are
  behavioral, not visual), so risk here is judged low but not
  independently re-screenshotted in RTL this cycle.

## Close-out

- `buttons_admin.html`'s tile-**reorder** gesture is HTML5
  `draggable`-based and fundamentally doesn't work on touchscreens at all
  (unrelated to this fix, found while reading the file) — filed as a new
  Backlog card, not fixed here.
- `e2e/tests-docs/lib.js`'s `algorithm` string undercounting its own
  fileset (see Nits above) — filed as a new Backlog card.

## Merge

Feature branch `fix/1170-touch-select-swallow-tap-add`. PR references
`Closes universaltill/ut-docs#1170`. `merge_method: "merge"` per
ut-docs#250 (never squash/rebase — GitHub's merge API re-attributes
squashed/rebased commit content to the merging account's real personal
email regardless of the commit's own `git config` author).
