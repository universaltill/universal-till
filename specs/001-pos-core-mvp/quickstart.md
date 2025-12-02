# Quickstart: Run POS Core MVP (local-first)

Prerequisites
- Go toolchain (recommended Go 1.25)
- Docker (optional for edge/dev environment)

Run locally (SQLite)

1. Copy example env and enable SQLite:

```bash
cp pos.env.example pos.env.dev
# ensure UT_STORE=sqlite in pos.env.dev
```

2. Build and run:

```bash
make build
UT_STORE=sqlite ./bin/unitill-pos
# open http://localhost:8080
```

3. Install a local plugin (example flow)
- Place plugin binary and manifest where installer expects, or use the plugin catalog UI to add a manifest URL.
- On install, host verifies manifest SHA256, creates `plugins` row and (if enabled) spawns plugin process for local mode.

Cloud-enhanced features (optional)
- Cloud features are opt-in. To enable cloud for a plugin, go to Settings → Plugins → Enable Cloud Mode and provide `ut_cloud_token` as instructed.
- Cloud features are asynchronous and do not block core sale flows.

Testing
- Run unit tests:

```bash
go test ./...
```

Agent context update
- After editing spec or plan files run:

```bash
SPECIFY_FEATURE=pos-core-mvp bash .specify/scripts/bash/update-agent-context.sh copilot
```
# Quickstart: POS Core MVP

**Date**: 2025-11-27  
**Scope**: Run and validate the POS core MVP with SQLite (offline-first).

## Prerequisites

- Go 1.25 installed.  
- SQLite support via `modernc.org/sqlite` (already in `go.mod`).  
- Environment file: copy `pos.env.example` to `pos.env.dev` (or set env vars inline).

## Run (SQLite)

```bash
make build
UT_STORE=sqlite UT_LISTEN_ADDR=:8080 ./bin/unitill-pos
# open http://localhost:8080 (till), /designer, /settings, /plugins
```

## Manual Test Flow (offline)

1) Disable network (if safe).  
2) Add items via Designer/catalog, including a weighed item and barcodes.  
3) Add to basket by barcode/SKU search; edit quantity; apply discount.  
4) Complete sale with split payments (cash + card).  
5) Verify DB (`data/unitill.db`) has `sales`, `sale_lines`, `payments`, `stock_movements`; confirm price_history appended on price change.  
6) Process a return linked to the sale and confirm stock increments and `sale_links`.

## Notes

- Money must remain integer minor units; weighed quantities are REAL.  
- No schema changes; `PRAGMA foreign_keys = ON` must be enabled.  
- Plugin entries should appear in UI after being stored in plugin tables.
