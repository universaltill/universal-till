# Code review — till default-theme 10-inch restyle (board #144, Phase A)

- **Date**: 2026-07-30
- **Branch**: `feat/till-ui-10inch-restyle`
- **Card**: ut-docs #144 (Farshid's field report — "till UI not good on the
  10-inch screen, everything should be larger + better style"). Direction
  he chose: **Both** (restyle default now + curated theme plugins later),
  style **"you decide"** → clean, modern, high-contrast, large-touch
  (Square/Loyverse-ish). Phase B (theme plugins) is follow-up card #145.
- **Scope (Phase A)**: CSS-only, `web/public/app.css` (the default theme).

## What changed

A coherent "large-touch" pass on the default theme: `html` base 16→17px
(gentle global +6%, everything is rem-based); product-tile grid
`minmax(118px→150px)` with `.thumb` 54→72px and `.tile-name`/`.tile-price`
→ .95rem and a `.btn-tile` `min-height: 8.5rem`; primary `.btn` padding up
+ `min-height: 3rem`, font .9→1rem; touch-menu tiles bigger (minmax
160→190, min-height 130→160, icon 2.4→2.9rem, label 1.05→1.2rem). No
palette/aesthetic overhaul — sizing/spacing/contrast only. The runtime
per-till UI-scale (0.7–2.0) still layers on top.

## Verification

- `go build ./...`, `guard-i18n.sh`, `internal/pages` tests — green
  (CSS-only; Go tests don't cover it, so the check is layout reasoning +
  a real render).
- **Real driven run at 1280×800** (headless Chrome against the built
  binary, demo catalog): sale screen shows large product tiles with
  images, readable names/prices, big high-contrast Cash/Card/Gift/QR
  tender buttons, clear basket; touch menu tiles roomy. No clipping.
  Screenshots delivered to Farshid to judge on the real device.

## Independent (opus) review — ship-able Phase A, no blockers

The reviewer confirmed the change is internally coherent (all rem-based,
symmetric padding, no physical-direction props → RTL-safe) and that the
sale/checkout path, the self-order kiosk (its own scoped classes +
`body.kiosk` overrides), and the basket zero-height-collapse history are
all unaffected. Two should-fixes on **admin** surfaces, both from the
global 17px bump — **fixed before commit**:

1. `.vg-cols` variant editor: its 8 fixed-rem columns sum grew past a
   1280px screen (the target device), and the `overflow-x:auto` guard only
   armed at ≤900px — so 900–1289px was unprotected. **Fixed**: raised that
   guard to ≤1300px so the variant grid scrolls horizontally instead of
   overflowing the page on a 10-inch screen.
2. `.btn { min-height: 3rem }` leaked into `.btn-actions .btn` (compact
   catalog-row action buttons), inflating row height ~70% — the author had
   protected `.btn-x`/`.held-chip` but missed this one. **Fixed**: added
   `min-height: 0` to `.btn-actions .btn` to keep rows compact.

Accepted nits (not fixed — pre-existing/low-value): the product grid's
`minmax(150px)`/`thumb 72px` are px (don't track UI-scale like the rest of
the tile) — pre-existing px/rem mix, amplified but not introduced here;
`.btn-tile 8.5rem` also applies to the button-layout admin editor
(makes editable tiles taller — acceptable for a touch theme).

## Note

Aesthetics are Farshid's call (he said "you decide"); this ships the
objective sizing/contrast baseline. He should judge the look on the real
10-inch device — proportion tweaks are a cheap follow-up. Curated theme
plugins = #145.
