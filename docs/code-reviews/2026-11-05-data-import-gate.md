# Review: permission gate of POST /api/data/import (ut-docs#720)

Date: 2026-11-05 · Branch: `fix/720-data-import-gate` (+ ut-docs
`docs/720-data-import-gate`) · Complexity: easy
Built by: Sonnet subagent (BA/Dev) · Tested by: separate Sonnet subagent ·
Reviewed by: Opus 5.5 subagent (different model from the author).
Architect phase folded into the Dev brief for this easy card: the design was
settled by the BA's evidence (no code path changes, no migration, no ADR).

## Decision

The card asked whether `POST /api/data/import` should move from
`import_export` to `data_management` to match `/api/data/export`. Kept on
`import_export` (option 2 of the card). The reviewer re-read the other
import entry points: `POST /api/import` and `/api/import/sample-data`
(`import_page.go`), `POST /api/import/from-file` (`import_file_rows.go:264`,
whose doc says it and `/api/data/import` reach the same file-rows run) and
`POST /api/import/from-system` (`import_from_system.go:141`, the page sibling
of api-source entries) are all `import_export`. Moving only the JSON API to
`data_management` would let a custom role run an import through the /import
page but not through the API for the same entry, and would make the
permission-screen descriptions wrong (`import_export` is "import products
and other data from a file"; `data_management` is backup/restore/clean-up).
`/api/data/export` is the Settings → Data sales-range download and stays
`data_management`. The asymmetry is intended. Seeded roles hold both
actions, so the decision changes nothing for any real till.

## What shipped

- `internal/pages/import_dispatch.go`: the doc comment no longer claims "same
  manager gate" as `/api/data/export`. It names `import_export`, lists the
  sibling import routes and says why export differs (ut-docs#720). No code
  change.
- `internal/pages/import_menu_manager_gate_test.go`:
  `TestDataImport_GatedByImportExportNotDataManagement`: a custom role with
  only `import_export` gets past the gate, and one with only `data_management`
  gets 403.
- ut-docs `reference/plugin-manifest.md` §`import.requested.ask`:
  "manager-gated" becomes "`import_export`-gated".

## Findings (Opus review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Nit | The new comment said `/api/import` "run[s] the same imports" as the plugin route. `/api/import` is the hardcoded core importer, and `/api/import/sample-data` was missing from the list | Fixed: the comment now separates the core routes from the /import page's routes into the same plugin import runs |

Nothing else found. No other comment says the two routes share a gate.
`internal/data/plugin_repo.go:2621` says "manager-gated", which is still
true. The demo-mode deny list already covers `/api/data/import`. The test
sends no body: it checks the gate only (the import_export role then gets a
non-403 parse error), and that is all it needs to check.

## Verification

- TDD re-verified by the reviewer in a throwaway `git worktree`, not the
  shared checkout. With the gate flipped to `data_management`, the new test
  fails with `import_export-only role = 403, want past the gate:
  {"data":null,"error":"manager only"}`. On the branch it passes, and so does
  `TestImportExportEndpoints_RealSessionGatesByRole`. After the comment fix,
  `gofmt -l` is empty, `go vet ./internal/pages/` is clean and the gate
  tests pass.
- Tester: flip check reproduced independently. `gofmt`, `go build`,
  `go vet`, golangci-lint (0 issues), `shellcheck scripts/ci/*.sh` (0) and
  all 144 `bash scripts/...` steps of ci.yml's build job pass. The one
  `go test ./... -race` run timed out after 10m in catalogpull, catsync,
  cloudsync, db, pages and plugins, and `internal/data` failed a wall-clock
  perf assertion. None of these packages is in the diff, and none printed a
  DATA RACE. All of them pass without `-race`, the way CI runs them (CI does
  not use `-race`).

Pre-flight: help topic `n/a (no user-visible behaviour change; help topics
don't name actions)`; docs-shots `n/a`; `reference/` `updated`
(plugin-manifest.md names the action); README `n/a`; en.json/meta `n/a (no
keys)`; migration `n/a (inert for seeded roles; 001_init.sql frozen)`;
follow-up `none`.

## Verdict

Approve. Nothing blocking, no deferred items.
