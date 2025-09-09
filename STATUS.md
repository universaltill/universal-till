# Project Status

Updated: now

## Built
- Edge runtime (Go) with HTML UI (htmx), i18n, themes (Default, Monarch)
- Buttons/products: add/remove in Designer; images via URL or /public/images
- Basket and tender; barcode auto-capture (USB HID), quantity support
- Settings page: currency, country, region, tax mode (inclusive), tax rate
- Currency formatter; TaxEngine (percent-based, inclusive/exclusive)
- Storage: file JSON (default) or SQLite (UT_STORE=sqlite). JSON→SQLite auto-migration
- Theme selector stored in DB; loads on startup
- Serve local reference images via /samples when UT_SAMPLES_DIR is set
- Dockerfile + docker-compose.edge.yml (persistent volume)

## How to run
- Local: `make build && ./bin/edge`
- With SQLite: `UT_STORE=sqlite ./bin/edge`
- Docker: `docker compose -f docker-compose.edge.yml up --build`
- Open: `/` (till), `/designer`, `/settings`

## Key env vars
- UT_STORE=sqlite | UT_DEFAULT_LOCALE | UT_CURRENCY | UT_TAX_INCLUSIVE | UT_TAX_RATE | UT_SAMPLES_DIR
- See `edge.env.example` / `edge.env.dev`

## Repos/Dirs
- universal-till/cmd/edge (runtime), cmd/store (WIP store API)
- internal/{pos,ui,httpx,common}
- web/ (templates, assets, themes)
- plugins/ (structure + example manifest)
- design/ ADRs

## Next up (suggested)
- Themes: add OSPOS/Floreant presets; refine Monarch spacing/contrast
- Store: extract to new repo; Azure/AWS upload, signed bundles, edge installer
- Plugins: tax/currency country modules; hardware/payment provider plugins
- Items: CRUD with images upload (not just URL); search/find item list UI
- Multi-device sync: cloud sync protocol (cloud-proto); user auth/subscriptions

## Notes
- Settings are persisted (SQLite if enabled, else data/settings.json)
- Buttons JSON auto-migrates to SQLite on first run with UT_STORE=sqlite
