# Tasks – Refactor Page Bootstrap (from `main_old.go`)

## T1 – Map routes and dependencies
- [ ] List all routes/endpoints in `main_old.go` (pages, API, plugin actions, static) and match them to target files under `internal/pages/`.
- [ ] Identify settings/menu/plugin record data flows currently in `internal/old_common` and how they’re used by handlers.
- [ ] Note dependencies: `ui` renderers, POS engine usage, plugin file storage, i18n/currency init, button store.

## T2 – Design updated pages package
- [ ] Define structure of `internal/pages` (handler files for core pages, POS API, plugin API, static, health).
- [ ] Specify `pages.Init(ctx, cfg, pm, db)` signature and outputs (`*http.ServeMux`), including dependency creation (i18n, currency, settings store, button store, pos engine).
- [ ] Plan menu/theme injection for all renders; decide helper(s) for menu building and settings access.

## T3 – Implement refactor
- [ ] Move handlers from `main_old.go` into appropriate `internal/pages` files; ensure responses/paths match legacy behaviour.
- [ ] Migrate settings/menu/plugin record logic from `internal/old_common` into the new structure (or dedicated package) without behaviour changes.
- [ ] Wire plugin manager from `internal/plugins/plugins.go` into pages for menu plugins, records, and state.
- [ ] Update `main.go` to use slim init flow (`configuration.Init()`, `plugins.Init()`, `pages.Init()`, `Start()`), deprecating `main_old.go`.
- [ ] Verify compilation and route registration parity; adjust imports/build as needed.
