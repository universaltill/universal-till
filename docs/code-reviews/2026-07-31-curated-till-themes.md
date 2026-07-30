# Code review — curated built-in till themes Fresh/Amber/Slate (board #145)

- **Date**: 2026-07-31
- **Branch**: `feat/theme-fresh` → PR #110
- **Card**: ut-docs #145 (Phase B of #144). Farshid supplied 8 reference
  images (#146) — "you should create themes like these images" — asking for
  curated themes matching those looks.
- **Scope**: three new CSS themes under `web/public/themes/`, plus a test
  fix. No Go behavior change; the default theme (`app.css`, #144) is
  untouched, and no default is changed — all three are opt-in.

## What shipped

Three curated themes, each a coherent modern-POS look keyed to one
reference image:

- **Fresh** (`fresh.css`) — sky-blue accent (`--accent:#0ea5e9`), rounded
  photo-tile cards with soft shadow, bold min-3.4rem tender buttons, navy
  nav (ref7).
- **Amber** (`amber.css`) — warm orange accent (`--accent:#f97316`) on a
  cream background, photo tiles (ref3).
- **Slate** (`slate.css`) — navy accent (`--accent:#1d4ed8`), **text-only
  tiles** (`.btn-tile .thumb{display:none}`) with a left accent bar and a
  denser `minmax(130px)` grid, for photo-less grocery/retail catalogs (ref1
  Opencloud look).

Each is a pure `:root` token override plus a handful of component tweaks —
the same shape the existing `monarch.css` built-in and plugin themes use.
They're picked up automatically: `availableThemes` unions the on-disk
`web/public/themes` dir with the embedded `public/themes` FS, so dropping a
`*.css` file in is all that's needed — no Go wiring, no migration.

## Verification

- `go build ./...`, `go vet`, `guard-data-access.sh`, `guard-i18n.sh` — all
  green (CSS is not i18n/SQL-bearing; guards confirm nothing else drifted).
- Full `internal/pages` package test — green.
- **Real driven render at 1280×800** (headless Chrome, built binary, demo
  catalog) for all three: Fresh and Amber show large photo tiles with
  readable names/accent-coloured prices and high-contrast tender buttons;
  Slate shows compact text tiles (no thumbnails), navy accent bars, more
  products per screen. No clipping. Screenshots delivered to Farshid.

## Test fix — de-brittled the themes assertion

Adding the three CSS files broke `TestThemesHandler_ServesBuiltinAndPluginCSS`,
which hardcoded `len(opts)==2` and a fixed `[monarch, midnight]` order — it
assumed monarch was the *only* embedded built-in. That assumption is exactly
what #145 changes. Rewrote it to assert the real contract instead: built-in
themes are sorted and all precede any plugin theme, monarch is present as a
built-in, and the `midnight` plugin theme carries its plugin-id source. It
still fails on a genuine regression (built-ins mis-sorted, a plugin theme
ordered before a built-in, or the plugin source wrong) — verified by reading
the ordering/source guards, not just that it now passes. Robust to future
built-in themes being added.

## Notes / follow-ups

- Aesthetics remain Farshid's call — he judges the three looks on the real
  10-inch device and can ask for palette/proportion tweaks (cheap CSS
  follow-ups) or more themes from the remaining reference images.
- These are built-in themes in core (simplest path for a curated set that
  ships with every install); if the set grows large or wants independent
  release cadence, a `ut-plugin-theme-*` repo is the plugin-first home —
  not warranted for three first-party themes today.
- Below the release-cadence threshold on its own (feature merges since the
  last tag < 5, no p1); rides the next release rather than cutting one now.
