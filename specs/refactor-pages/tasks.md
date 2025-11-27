# Tasks – Refactor Page Bootstrap (from `main_old.go`)

## T1 – Map routes and dependencies
- [x] List all routes/endpoints in `main_old.go` (pages, API, plugin actions, static) and match them to target files under `internal/pages/`.
- [x] Identify settings/menu/plugin record data flows currently in `internal/old_common` and how they’re used by handlers.
- [x] Note dependencies: `ui` renderers, POS engine usage, plugin file storage, i18n/currency init, button store.

## T2 – Design updated pages package
- [x] Define structure of `internal/pages` (handler files for core pages, POS API, plugin API, static, health).
- [x] Specify `pages.Init(ctx, cfg, pm, db)` signature and outputs (`*http.ServeMux`), including dependency creation (i18n, currency, settings store, button store, pos engine).
- [x] Plan menu/theme injection for all renders; decide helper(s) for menu building and settings access.

## T3 – Implement refactor
- [x] Move handlers from `main_old.go` into appropriate `internal/pages` files; ensure responses/paths match legacy behaviour.
- [x] Migrate settings/menu/plugin record logic from `internal/old_common` into the new structure (or dedicated package) without behaviour changes.
- [x] Wire plugin manager from `internal/plugins/plugins.go` into pages for menu plugins, records, and state.
- [x] Update `main.go` to use slim init flow (`configuration.Init()`, `plugins.Init()`, `pages.Init()`, `Start()`), deprecating `main_old.go`.
- [x] Verify compilation and route registration parity; adjust imports/build as needed.
