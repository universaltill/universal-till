# Universal Till

Ultra-light, offline-first EPOS written in Go. Themeable HTML UI, plugin-ready, designed to run on Raspberry Pi and small ARM devices.

## Quick start (edge)

```bash
make build && ./bin/edge
# open http://localhost:8080
```

## Environment variables
Copy `edge.env.example` to `edge.env.dev` and adjust:

- `UT_LISTEN_ADDR` – default `:8080`
- `UT_DEFAULT_LOCALE` – default `en`
- `UT_STORE` – `sqlite` enables embedded DB (recommended)
- `UT_SAMPLES_DIR` – optional path to images to serve at `/samples`
- `UT_CURRENCY` – currency code (e.g., `GBP`, `USD`)
- `UT_TAX_INCLUSIVE` – `true|false`
- `UT_TAX_RATE` – integer percent (e.g., `20`)

Run with Docker Compose (loads `edge.env.dev`):

```bash
docker compose -f docker-compose.edge.yml up --build
```

## Themes
- Open Designer: http://localhost:8080/designer
- Choose a theme (Default, Monarch). Stored locally and applied automatically.

## Data & Migration
- Buttons default: `data/buttons.json`
- SQLite: `data/unitill.db` when `UT_STORE=sqlite`
- First SQLite run imports `buttons.json` → `buttons.json.migrated`

## Settings
- System settings at `/settings` (currency, country, region, tax)
- Saved in DB and applied immediately

## Barcode
- USB HID scanners work automatically (global key buffer + Enter)
- Quantity supported via form or JSON `qty`
