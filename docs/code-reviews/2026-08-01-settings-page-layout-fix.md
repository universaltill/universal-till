# Code review: settings page layout (overflow cards, payment fee rows)

**Date:** 2026-08-01
**Scope:** `web/public/app.css`, `web/ui/pages/settings.html` (pure CSS/template, no Go changes)
**Card:** universaltill/ut-docs#81

## What shipped

The `/settings` page (`.settings-grid`) uses CSS multi-column masonry
layout (`columns: 24rem`), a deliberate earlier fix for "random spaces
between boxes". Two cards contain wide tables — the Backup file list and
the raw "All Settings" key/value table — that were overflowing with an
internal horizontal scrollbar instead of using the page's full width,
since CSS multi-column has no per-card "span N of the current column
count" concept. Separately, the Payments card's per-provider fee row
(`.fee-row`) had zero CSS anywhere, so a long provider name (e.g. "Card
(Stripe)") wrapped mid-row while shorter names happened to fit by
accident.

Fix:
1. A new `.settings-wide` class (`column-span: all`) applied to the two
   wide-table cards — the CSS-multi-column-native way to go full-width
   without switching the whole grid to CSS Grid, which would undo the
   masonry packing every other card relies on.
2. New `.fee-row` CSS Grid layout so every provider row lays out
   consistently regardless of name length.

## Independent review (different model) — real findings, both fixed

The first draft of `.fee-row` (`grid-template-columns: minmax(6rem,1fr)
3.6rem auto 3.6rem auto`, plain `inline-size: 3.6rem` inputs) fixed the
reported wrap bug but introduced two real regressions, both verified live
against the running app (not just read off the CSS):

1. **Blocking — input value clipping at every viewport/UI-scale.**
   `PercentMaj`/`FixedMaj` are formatted `%.2f`
   (`internal/pages/settings_page.go`), so a 100% fee renders as the
   6-character `"100.00"` — the field's own documented max. At `3.6rem`
   with the base `.5rem .65rem` input padding, the content box only fit
   ~3 characters, clipping every real value (even the `0.00` placeholder
   → `0.0`). Because both the input width and the font are rem-based,
   this held at every viewport and every `--ui-scale` (1/1.5/2) — not a
   narrow-screen-only bug.
2. **Should-fix — Save button clipped out of reach below ~430px.** The
   row's fixed-rem tracks gave it a wider natural width than some narrow
   viewports, and the ancestor `.card` uses `overflow: hidden` (needed to
   keep masonry columns tidy) — with no scroll escape on `.fee-row`
   itself, content past the card's edge was simply gone, not scrollable.
   At 320px the Save button was 0px visible. This was a **regression**:
   before this diff the row wrapped onto a second line instead, which
   kept the button reachable (confirmed by re-testing with the new rules
   removed at runtime).
3. Reviewer also confirmed RTL correctly mirrors (logical properties
   throughout, no `left`/`right`/`width`) and that clipping is *worse* in
   RTL — the input shows the trailing digits instead of the leading ones
   (`100.00` → `.00`), same root cause as #1.

## Fix applied

- `.fee-row`'s number-input columns widened `3.6rem` → `4.8rem`, with a
  dedicated reduced `padding: .35rem .4rem` on those inputs (mirrors the
  existing `.vg-row input[type="number"]` pattern in this same
  stylesheet, used for the variants grid's own fixed-width-input rows).
- `.fee-row { overflow-x: auto; }` added — the same pattern this
  stylesheet already uses for wide tables inside a card
  (`.settings-grid .card .table { display: block; overflow-x: auto; }`,
  a few lines above). A `.fee-row` narrower than its content now scrolls
  internally rather than being silently clipped by the card's own
  `overflow: hidden`.

## Verification (after the fix, re-run for real)

- `go build ./...`, `go vet ./...` — clean (no Go changes in this diff).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` — both pass.
- `go test ./internal/pages/... ./internal/data/...` — pass.
- Real running app (built binary, `UT_AUTH=off`, real Chromium via
  Playwright) driven at 320/390/430/820/1280px, `--ui-scale` 1/1.5/2, and
  `?lang=fa` (RTL), with `percent=100.00` / `fixed=999.99` injected into
  a real fee row:
  - No input clipping at any combination (`scrollWidth <= clientWidth`
    for both number inputs, checked programmatically, not eyeballed).
  - Below ~430px the row now scrolls (`overflow-x: auto`) and the Save
    button is reachable at max scroll (`getBoundingClientRect()` fully
    inside the card's box after scrolling) — confirmed with an isolated
    browser context per test case, after an earlier test run using a
    single reused page across viewport changes gave a false negative
    (Chromium's bfcache appears to have carried a `--ui-scale` inline
    style across an in-page `goto`, corrupting that specific
    measurement; re-run with a fresh context per case to rule it out).
  - Both `.settings-wide` cards confirmed as **direct children** of
    `.settings-grid` in the live DOM (required for `column-span: all` to
    take effect) — not just correct in the template source.
  - Seeded 4 real backup files: Backup-table card renders full-width, no
    internal horizontal scrollbar, all rows/buttons legible.
  - No masonry-packing regression on any other card (full-page
    screenshots at 900px/1280px).
- Full existing e2e suite (`e2e/tests/`, default project): 30/31 pass.
  The one failure, `catalog-image-to-till.spec.ts`, is a pre-existing,
  unrelated flake (image-load timing) — independently confirmed via
  `git stash` to fail identically against unmodified `main`.
- `settings-osk.spec.ts` (the closest existing settings-adjacent spec):
  5/5 pass.
- Zero browser console/JS errors throughout.
- No real client/shop name or secret-shaped literal introduced.
- `column-span` browser support: no browser-support floor is documented
  anywhere in `ut-docs`/`universal-till` (a pre-existing documentation
  gap, not introduced here). Target runtimes per this ecosystem's ADRs
  (Chromium kiosk mode, webkit2gtk-4.1, WKWebView/WebView2, Android
  WebView) all comfortably postdate `column-span: all`'s baseline
  support (Chrome 50+/Safari 9+/Firefox 71+) — verified live on Chromium
  only, reasoned (not tested) safe for WebKit given no WebKit build was
  available in this sandbox.

## Deferred (new Backlog card, not blocking this merge)

No e2e regression test exists yet for either bug class this review
caught (`.fee-row` clipping / narrow-viewport reachability), despite the
repo already having tests of exactly this shape
(`tender-panel-reachable.spec.ts`, `ui-scale-basket.spec.ts`). Both are
trivially assertable (`scrollWidth <= clientWidth`, button rect inside
card rect). Filed as a follow-up rather than expanding this diff further.

## Verdict

Safe to merge. Both real findings from independent review were fixed and
re-verified live, not just patched and assumed.
