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
