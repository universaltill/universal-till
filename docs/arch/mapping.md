# Architecture mapping: Constitution → Repo layout

This short file maps the project constitution terminology to the actual repository layout. Use this as the authoritative mapping when working from the constitution or giving instructions to AI agents.

- Constitution term: `core/`  → Repo path: `internal/pos`
  - Notes: pure domain logic (pricing, sales, inventory, aggregates) lives under `internal/pos` and is treated as the project "core" for development and testing.

- Constitution term: `app/` → Repo path: (no direct folder) `internal/*` use-case wiring
  - Notes: higher-level use cases are split across `internal/pages` (HTTP handlers) and `internal/plugins` (plugin orchestration). There is no single `app/` directory; treat `internal/pages` + controller code as the application layer.

- Constitution term: `adapters/` → Repo path: `internal/db`, `internal/server`, `internal/plugins` (DB adapters, server wiring, plugin runtime)

- Constitution term: `ui/` → Repo path: `web/` + `internal/pages`
  - Notes: templates and assets live in `web/ui` and static files in `web/public`. Handlers that render UI are in `internal/pages`.

- Constitution term: `plugins/` → Repo path: `internal/plugins`

Why this mapping
- The constitution is normative for design principles; the repository predates the constitution's folder naming. This document avoids forcing a large refactor by providing an explicit, discoverable mapping that preserves the constitution's intent while matching the codebase.

When to update
- If files are reorganised or a refactor introduces `core/`, `app/`, or `adapters/` folders, update this mapping and notify maintainers. Any changes to this mapping are governance-level changes and should be recorded in the constitution change log.
