# ut-docs#1173 — table designer overflows the till's real 1280x800 viewport

**Branch:** `fix/1173-table-designer-viewport-overflow` · **Date:** 2026-08-28

## What shipped

Two-part layout fix plus a shared-helper cleanup found while verifying it:

- `web/ui/pages/tables.html`: the floor-plan SVG (square `viewBox`) was sized
  `inline-size:100%; block-size:auto`, so on a landscape viewport it rendered
  as tall as it is wide. Bounded `max-block-size: min(30rem, 55vh)` (same
  `min(rem, vh)` convention as `.catalog-list #catalog-table`), kept
  `inline-size: auto` so the smaller of the two constraints wins, and added
  `margin-inline: auto` to keep it centred (LTR and RTL) once it's narrower
  than its card.
- `web/public/app.css`: while measuring the above, found the shared
  `.users-list` class (used by 8 admin pages) blows out its CSS Grid track
  at 1280px — a grid item's default `min-inline-size` is `auto`, and this
  page's inline-editable row (several inputs + a select + two buttons) is
  wide enough to force the whole `2fr 1fr` grid wider than the viewport.
  Added `min-inline-size: 0` + `.users-list .table { display: block;
  overflow-x: auto }`, mirroring the already-reviewed `.settings-grid .card`
  / `.settings-grid .card .table` pair (ut-docs#251).
- `web/ui/pages/tables.html` (second pass, review finding): capping
  `.users-list`'s table width made this page's own row — the widest of the
  8 `.users-list` consumers — push its own Save button behind an in-card
  horizontal scroll with no visible affordance on a touchscreen. A blanket
  `.users-inline { flex-wrap: wrap }` was tried and reverted elsewhere for
  breaking `/users`' role-select+button row (app.css's own ut-docs#898
  note); scoped here to `.users-list .users-inline` inside `tables.html`'s
  own `<style>` block instead, which physically cannot reach any other
  page's DOM.
- `web/ui/pages/country_settings.html`: removed a now-redundant manual
  `overflow-x:auto` wrapper div (nested scroller once `.users-list .table`
  carries its own).
- `e2e/tests/tables-designer-viewport-1173.spec.ts`: viewport regression
  test at the till's real 1280×800 logical resolution.
- `e2e/tests/helpers.ts`: extracted `deactivateAllTables`/`createTable`,
  found duplicated verbatim across four `tables-*` spec files (826, 1170,
  1025, and this one); the other three specs now import the shared version
  instead of carrying their own copy.
- `web/help/img/**`, `web/help/img/manifest.json`: regenerated via `make
  docs-shots` (guard-docs-shots requires fresh screenshots whenever
  `web/ui/**`/`web/public/**` changes). Changed: `tables`, `country-settings`
  and `promotions` (all three genuinely render differently — verified below);
  `users` (fa/tr) and `invoices` (tr) also picked up drift from unrelated
  `main` changes since the manifest was last regenerated.

## Root cause, established by direct measurement

Reverting the fix and running the new spec against the real served app
(headless Chromium) reproduced the reported symptom to the pixel: the plan
rendered **1206.59px tall on an 800px-tall viewport** — more than the whole
screen, before the add-table form or table list even start. A second,
independent overflow surfaced only once the first was fixed: the page's
`document.documentElement.scrollWidth` was still 1412–1419px at a 1280px
viewport — traced (via direct `getBoundingClientRect` probes against the
live app, not by reading the CSS) to the `.users-list`/`.users-form` grid
blowout described above, confirmed by checking `getComputedStyle(...).
gridTemplateColumns` directly (`1120px 258px` on a 1235px-wide grid — the
grid was rendering wider than its own container).

## TDD / false-pass verification (author's own)

Both regressions were reverted and re-measured on the real running app
before being called fixed:

1. Floor-plan height: reverted → `1206.59px` reported, matching the ticket's
   description almost exactly; restored → passes (`<600px`).
2. Page-level horizontal scroll (grid blowout): reverted the `.users-list`
   CSS only → `scrollWidth 1412` vs `1280` viewport; restored → passes.
3. Row Save-button reachability: **first version of this check was a false
   pass** — `.click()` alone succeeded whether or not the wrap fix was
   present, because Playwright auto-scrolls an element's nearest scrollable
   ancestor into view before clicking, which defeats the whole point of the
   check (a real touchscreen operator has no such auto-scroll). Replaced
   with the `scrollWidth`-vs-`clientWidth` pattern `basket-no-horizontal-
   scroll-391.spec.ts` already uses for the identical bug class. Reverted
   just the `.users-list .users-inline` wrap rule → real red (`scrollWidth
   1095` vs `clientWidth 778`); restored → green.

## Gates

- `gofmt` clean, `go build ./...` / `go vet ./...` clean (no Go changes —
  CSS/template/test only).
- `go test ./internal/pages/...` (the package the templates render through):
  pass.
- Full CI-blocking guard list (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `check-brand-assets`,
  `guard-makefile-version`): all pass.
- Targeted e2e regression (11 tests): the new spec, all of `tables-tap-to-
  add-1025`, `tables-keyboard-reposition-826`, `tables-touch-drag-1170`
  (the other `.users-list`/floor-plan consumers), and `locations-registers-
  auth-901` (a second `.users-layout` page, sanity-checked for the shared
  CSS change) — all pass. Did not re-run the full ~200-spec suite a second
  time in this session; the targeted set covers every spec that exercises
  the changed CSS surface.

## Independent review (Opus, fresh context, different model from author)

Built the app, ran the new spec against a real till in Chromium, and
measured all 8 `.users-list` consumers before/after by diffing against the
pre-fix `app.css`. Findings, all addressed before merge:

1. **Blocker — `guard-docs-shots` red as first committed.** Both changed
   files are inside the guard's hashed surface, and the `/tables` and
   `/country-settings` screenshots genuinely change. Fixed: `make
   docs-shots` run and the regenerated manifest/images committed.
2. **Should-fix — the per-row Save button was fully off-screen behind an
   undiscoverable in-card scroll**, colliding with the ticket's own
   "controls reachable" criterion and the exact failure class ut-docs#1021/
   #1170 exist for. Fixed: scoped `.users-inline` wrap (above), verified
   red→green with the corrected (non-false-passing) test.
3. **Nit — `display: block` on `.users-list .table` shrink-to-fits narrow
   tables** (`/locations` 778→702px, `/promotions` 778→733px, `/registers`
   header-only 777→255px) as a side effect of the same fix that also
   repairs `/country-settings`' own pre-existing overflow (1313→1280px).
   Cosmetic, matches the already-reviewed `.settings-grid` precedent
   exactly — accepted as-is, noted here per the reviewer's own
   recommendation rather than treated as a silent side effect.
4. **Nit — dead wrapper.** `country_settings.html`'s own manual
   `overflow-x:auto` div became a redundant nested scroller. Removed.
5. **Test nits** — `viewportSize().width` instead of a hard-coded `1280`;
   the `500`px height threshold left too little headroom against a
   plausible future cap retune (widened to `600`, still >2x margin below
   the real regression class); the `deactivateAllTables`/`createTable`
   duplication (now four spec files) was worth extracting rather than
   adding a fifth copy. All applied.

Also checked and found **not** a problem: `min-inline-size: 0` letting
non-table `.users-inline` content (e.g. `fiscal_register.html`'s address
form) spill out uncontained — inputs there are fixed-width flex items that
shrink normally, verified down to 940px with German-length labels.

**On the ticket's "verified by screenshot from the device" criterion**: not
done — this cloud session has no 10.1" hardware, and only Chromium is
installed (no WebKitGTK, the actual ADR-0028 kiosk shell engine). The
reviewer's own assessment, which this record adopts rather than silently
narrowing scope: the automated 1280×800 logical-resolution check is strong,
pixel-matching evidence for the layout half, but it structurally cannot see
(a) WebKitGTK-specific rendering of `inline-size:auto` on an SVG root with
only a `viewBox` (no other in-repo precedent for that exact construct), or
(b) real-finger reachability of the in-card horizontal scroll on
`.users-list` for the other 7 pages, unchanged by this fix's `.users-inline`
wrap. The device-screenshot acceptance box on ut-docs#1173 stays explicitly
open rather than being marked done.

## Deferred / adjacent

- Real physical-panel verification (Pi 5 + Waveshare 10.1", the same
  hardware ut-docs#1166 needs) is out of reach for a cold cloud session —
  flagged on the issue rather than claimed.
- The `.users-list`/`.users-form` grid-blowout class of bug is now guarded
  against for all 8 `.users-layout` consumers by this fix, not just
  `/tables` — worth keeping in mind if a future page in that family grows a
  wide row of its own.
