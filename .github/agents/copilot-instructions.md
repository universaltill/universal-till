<!-- Mirror of .github/copilot-instructions.md for repo agent scripts -->
# Copilot instructions — Universal Till

Purpose
- Provide short, actionable guidance so AI coding agents (and contributors) can be productive immediately.

Quick commands
- Build: `make build` (produces `bin/unitill-pos`) — see `Makefile`.
- Run locally: `./bin/unitill-pos` after `make build` or `go run ./...`.
- Tests: `make test` or `go test ./...` (unit tests live under `pos/`, `internal/` and `httpx/`).
- Docker: `docker compose -f docker-compose.edge.yml up --build` (edge development environment).

High-level architecture (what to know)
- Entry: `main.go` — initializes logging, config (`internal/config`), DB (`internal/db`), plugins (`internal/plugins`), pages (`internal/pages`) and starts the HTTP server (`internal/server`).
- HTTP UI & handlers: `internal/pages/` contains route registration and handlers (templates under `web/ui/`).
- Domain logic: `pos/` contains core POS domain code (pricing, sales, money types, tests).
- Plugins: `internal/plugins` implements a DB-backed plugin manager and catalog. Plugin install flow touches `internal/pages/plugin_api.go` and `internal/plugins/plugins.go`.
- Persistence: `internal/db` opens SQLite when `UT_STORE=sqlite` and runs migrations in `internal/db/migrations/`.

Patterns & conventions specific to this repo
- Package layout: `internal/` groups server-side modules; changes that affect routes or APIs usually live in `internal/pages` and `internal/plugins`.
- Handlers receive a dependency struct (see `internal/pages/init.go` / `deps`) — prefer adding dependencies there rather than global state.
- Database access: many modules use `database/sql` directly; use provided DB helpers in `internal/db.go` and follow existing `QueryContext` / `ExecContext` patterns.
- Migrations: SQL files are sequentially numbered in `internal/db/migrations/`. Avoid renumbering existing migration files.

Integration & runtime notes
- Settings are persisted and loaded via `internal/settings` (runtime config loaded at startup in `main.go`).
- Plugins are initialised with the DB and expose menu entries; `Manager.Reload` refreshes installed plugins (used after install endpoints).
- Static assets, templates and themes live under `web/public/` and `web/ui/` — UI changes usually require editing templates and `app.css`/`app.js`.

Developer workflow tips
- Use `make build` and `./bin/unitill-pos` for quick iterations; `docker compose -f docker-compose.edge.yml up --build` is useful to replicate the edge environment with env file `pos.env.dev`.
- Run a focused test package: `go test ./pos` or `go test ./internal/db`.
- When adding an HTTP route: update `internal/pages` (register in `pages.Init`) and add templates under `web/ui/pages` or `web/ui/partials`.

Files to inspect for changes
- `main.go` — startup order and wiring (config → db → plugins → pages → server).
- `internal/config/config.go` — environment-driven configuration.
- `internal/db/migration.go` & `internal/db/migrations/*.sql` — DB migration behaviour.
- `internal/plugins/plugins.go` & `internal/pages/plugin_api.go` — plugin lifecycle and install flow.
- `web/ui/layouts` & `web/ui/pages` — HTML templates used by handlers.

Scripts & agent integration
- The repo includes `.specify/scripts/bash/update-agent-context.sh` which looks for a Copilot/agent instruction file under `.github/agents/copilot-instructions.md`. If you edit this file, consider mirroring it there.

When to ask for human review
- Structural changes (DB schema, migration files, plugin data model) must be reviewed manually.
- Changes that affect startup wiring (`main.go`), settings, or plugin trust/installation should be flagged for maintainers.

If something's unclear, open a concise PR or ask in the repo: reference the changed file and give a short rationale.

— End —
