# 2026-07-25 — Physical keypad mapping tool (ADR-0021)

## Context
Farshid asked for support for physical custom keyboards — dedicated
hardware keypads (Tesco/restaurant-style), a grid of buttons where each
key represents a specific item. Research (see ADR-0021) found this
already mostly works: almost every programmable keypad is a USB-HID
keyboard-emulation device, and the cashier POS page's existing global
rapid-keystroke buffer (`web/public/app.js`, built for barcode scanners)
already submits any fast Enter-terminated keystroke sequence to
`/api/pos/scan` regardless of what has focus. A keypad programmed to
type "a code, then Enter" per key already works with zero backend
changes.

The one real gap: mapping many physical keys to many catalog items one
at a time through the full item-edit form doesn't scale to a shop with
dozens of PLU keys. This ships a purpose-built "keypad mapping" tool in
the catalog admin UI to close that gap.

## Design
Pure front-end addition to `web/ui/pages/catalog.html` — no backend
code touched at all. Reuses the already-shipped, unmodified
`POST /api/catalog/barcode` endpoint (the same one the item-edit form's
barcode field ultimately writes through) and the page's existing
`.pick-item` row-selection convention (already used by the image-upload
and label-printing panels on this same page). A `<details>` panel shows
the currently-selected item (set by clicking a catalog row, same as
picking an item to edit); a capture field, Enter-terminated exactly like
the existing `#item-barcode` field, POSTs `{barcode, itemId, isPrimary}`
via `fetch` and refocuses itself for the next key press.

## Independent review
Opus-model review, proportionate to a low-stakes additive change (no
money/security path, reuses a proven endpoint unchanged) but with real
scrutiny on the one genuinely unverified surface: the JS was reviewed by
eye only, since no browser-automation tool is available in this
environment to drive real `keydown` events.

**Confirmed correct:**
- `fetch` body encoding (`URLSearchParams` + form-urlencoded header)
  matches exactly what the Go handler's `r.ParseForm()` expects.
- No null-ref risk: every element the new JS reads is a static template
  node always present in the DOM (inside the `<details>`, rendered
  whether open or closed).
- Row-click integration is correctly ordered relative to the pre-existing
  generic `.pick-item` handler (this page's established convention for
  the image/labels panels), and the focus-stealing guard
  (`kpDetails.open`) correctly avoids yanking focus when the panel is
  closed.
- **Genuine finding, confirmed harmless**: pressing Enter in the capture
  field also reaches the page's global scan-buffer listener (event
  bubbles past `preventDefault()`, which doesn't stop propagation) — but
  that listener's target selector (`form[hx-post="/api/pos/scan"]`)
  doesn't exist on the catalog page at all, so it's a traced, harmless
  no-op. Same double-fire already exists for the pre-existing
  `#item-barcode` field; not a regression.
- Backend reuse claim verified by `git status`/`git diff`: zero lines
  changed in `internal/pages/catalog/handlers.go`,
  `internal/pos/catalog_ops.go`, or `internal/data/catalog_repo.go`.
- i18n guard passes; all `catalog.keypad.*` keys present in all 4
  locales with no drift.
- **A real, pre-existing gap found and traced, not fixed (out of
  scope)**: `CatalogRepo.AddBarcode`'s `ON CONFLICT` upsert never
  demotes a sibling barcode when a new one is marked primary, so an item
  can end up with two `is_primary=1` rows. Reviewer traced every
  consumer of `is_primary` and confirmed it's purely a display/ordering
  tie-breaker (`ORDER BY is_primary DESC LIMIT 1` for a representative
  label) — the actual scan-resolution query has no `is_primary` filter
  at all, so lookup correctness is unaffected. Cosmetic, pre-existing,
  predates this change; documented rather than fixed.

**Two low-severity documentation issues, fixed**: the ADR referenced
this review doc before it existed (now created); the hardware setup
guide stated two behaviors as observed fact ("the code appears and is
saved," "the item is added to the basket") that were only verified via
curl against the backend, never with a real browser driving real
`keydown` timing — softened to reflect what was actually tested, with
an explicit note to verify against real hardware.

## Verification
`go build ./...`, `go test ./...`, `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh` — all green (this change touches no Go
code, so these mainly confirm nothing else regressed).

Live-verified the full backend path against a real built binary: posted
a fake PLU code (`PLU4011`) for a real active item via
`/api/catalog/barcode` exactly as the new JS would, confirmed it landed
in `item_barcodes` with `is_primary=0`, then confirmed
`/api/pos/scan` with that exact code resolves and adds the item to the
basket — proving a keypad-style secondary code works identically to a
real barcode at the till. Also verified the "set as primary" checkbox's
`isPrimary=1` path persists correctly.

**Explicitly not verified**: real browser `keydown` event timing (no
browser-automation tool available in this environment) and real
physical-keypad hardware. This is called out in both the review above
and the shop-facing setup guide rather than glossed over.
