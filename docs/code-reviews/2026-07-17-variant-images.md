# Code review — variant images (2026-07-17)

Branch `feat/variant-images`. Farshid's ask, built to the spec
(`docs architecture/variant-images.md`).

- `POST /api/catalog/variant/image` — mirrors the item-image path: multipart,
  10MB cap, decode→PNG re-encode, path-safe ids, variant existence checked;
  saves `assets/items/{item}/variants/{variant}/thumb.png`; re-renders the
  panel.
- Variant grid gains an image cell: variant thumb with **onerror fallback
  chain** (variant → item image → hidden) + a 📷 upload control
  (hidden file input, auto-submits via htmx multipart).
- Grid 8 columns (CSS + scroll min-width), i18n key ×4.

Known limits (per spec): images are per-till until image LAN-sync is
designed (same as item images today); sale-screen/store surfacing uses the
item image until those tiles learn the chain (follow-up in spec).

Suites + both guards green; upload flow itself needs a click-through on the
next dmg (multipart htmx in the webview).
