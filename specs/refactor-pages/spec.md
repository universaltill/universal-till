# Spec: Refactor Page Bootstrap (from `main_old.go`)

Author: Core team  
Status: Draft  
Related files: `main_old.go`, `internal/pages/*.go`, `internal/old_common/*`, `internal/plugins/plugins.go`, `main.go`

## Problem
Legacy bootstrap logic in `main_old.go` wires routes, page rendering, settings, POS actions, and plugin handling in one place. This blocks the newer `internal/pages` structure and keeps `main.go` coupled to UI and plugin plumbing.

## Goal
Move page initialization (handlers/routes/UI wiring) into `internal/pages` so `main.go` stays slim:
```
configuration.Init()
plugins.Init()
pages.Init()
Start()
```
Behaviour must remain compatible with the current `main_old.go` endpoints and page flows.

## In Scope
- Extract all handlers/routes/UI logic from `main_old.go` into cohesive page modules under `internal/pages/` (index, designer, settings, plugins, FAQ, POS UI fragments, POS API endpoints, plugin install/download/delete, static serving, health).
- Introduce/expand `pages.Init(ctx, cfg, pm, db)` to register all routes and dependencies (i18n, currency, button store, POS engine, theme/settings).
- Move reusable settings/menu/plugin record logic out of `internal/old_common` into the new structure (or dedicated packages) without behaviour change.
- Use `internal/plugins/plugins.go` for plugin manager wiring (menu plugins, records) instead of legacy `old_common`.
- Keep UI rendering with `httpx.Render`, `ui.NewRenderer`, `ui.NewBasketView`, and existing templates under `web/`.
- Preserve plugin bundle handling (download/install/uninstall/delete, serve `/plug/<id>`, proxy `/ext/<id>`).
- Preserve POS API: scan, tender, basket rendering, buttons admin, settings save.
- Preserve static serving for `/public/` and `/samples/` (when configured).

## Out of Scope
- No schema changes.
- No redesign of templates or UI assets.
- No changes to POS engine logic beyond wiring.
- No new plugin capabilities—just refactor/wire existing behaviour.

## Functional Requirements (must-haves)
- `pages.Init` registers all routes now in `main_old.go` with identical request/response behaviour (paths, methods, payloads, HTTP codes).
- Menu construction still merges core menu items with plugin/menu records; theme from settings is honoured.
- Settings persistence (theme, currency, country/region, tax inclusive, tax rate, plugins) continues to work and reinitialises currency/tax resolver when saved.
- POS endpoints:
  - `/api/pos/scan` (supports form and JSON payloads, quantity handling) updates basket.
  - `/api/pos/tender` applies payment and returns basket view.
  - `/ui/buttons`, `/ui/basket`, `/api/buttons/add`, `/api/buttons/remove` operate with the button store.
- Plugin endpoints:
  - `/api/plugins/state|download|install|uninstall|delete`
  - `/plug/<id>` serve local plugin HTML
  - `/ext/<id>` proxy external menu plugins
- Static:
  - `/public/*` served from `web/public`
  - `/samples/*` served when configured
- Health: `/healthz` returns OK.
- `main.go` uses the new init flow; legacy `main_old.go` no longer needed at startup.

## Non-Functional
- No panic on missing plugin files; respond with appropriate HTTP codes as now.
- Remain offline-capable (no new network calls except existing plugin download/proxy).
- Keep logging parity where present.

## Acceptance Criteria
- `main.go` shows the slim startup sequence invoking `configuration.Init()`, `plugins.Init()`, `pages.Init()`, `Start()`.
- All routes from `main_old.go` are mounted via `pages.Init` under `internal/pages` without behavioural regression.
- Settings/theme/menu/plugins data flows migrated from `internal/old_common` into the new structure; `internal/old_common` no longer required for page bootstrap.
- Plugin manager usage goes through `internal/plugins/plugins.go`.
- Static, POS, and plugin endpoints respond identically (status codes, JSON/form support, template rendering) in manual smoke tests.
- Shortcut buttons use the `shortcut_buttons` table only; designer supports searching items (name/SKU/barcode) with thumbnails and adding/removing shortcuts directly (HTMX).
- Theme defaults to `monarch` and is persisted via settings store, read from config/env.
