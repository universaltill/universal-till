# universal-till — rules for working in this repo

The offline-first **POS host** (Go, SQLite, HTMX). Full standards: `docs` repo →
`reference/coding-standards.md`. The non-negotiables, some **mechanically enforced**
(CI will fail):

## Data access — repository pattern (enforced by scripts/ci/guard-data-access.sh)
- **Raw SQL lives only in `internal/data` (repositories) and `internal/db`
  (migrations).** No SQL query text anywhere else — CI fails on it.
- Threading `*sql.DB` / `*sql.Tx` through the domain layer is fine; writing the
  query outside the data layer is not. Add a `PluginRepo`/`POSRepo`/etc. method
  instead.
- Migrations under `internal/db/migrations/` are **append-only** after the first
  release (`001_init.sql` may still be edited pre-release).

## Offline-first (non-negotiable)
- **Checkout must never be blocked by the network.** A full sale completes offline.
- Surface offline/sync/install state with status chips/banners, never modal
  blockers in the kiosk flow. Status/lock/exit must always be reachable.

## API, formats, i18n
- Responses `{ "data": …, "error": null }`; JSON **snake_case**; dates ISO-8601;
  **money = integer minor units**.
- **No hardcoded user-facing strings** — use locale files under `web/locales`.
- Validate all external input (users, plugins, devices).

## Plugins
- Installed plugins are Ed25519-verified before they run
  (`internal/plugins/manifest_verifier.go`). Never run an unverified plugin.

## Before committing
- `go build ./... && go test ./...` and `bash scripts/ci/guard-data-access.sh`.
- Feature branch; code review recorded in `docs/code-reviews/<date>-<topic>.md`;
  then merge to `main`. No secrets in logs or committed files.
