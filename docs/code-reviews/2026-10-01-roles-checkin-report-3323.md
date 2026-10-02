# Code review: main till reports its roles and permission actions on check-in (ut-docs#3323)

- **Date:** 2026-10-01
- **Card:** ut-docs#3323 (ADR-0128 §5), split from #3165
- **Author:** pipeline lane `lane:cloud-54` (Opus 5.5 dev subagent)
- **Reviewer:** independent Fable subagent (a different model from the author)

## What shipped

- `internal/data/auth_repo.go`: `AuthRepo.RolesReport` reads every `roles` row, its granted actions and all `permission_actions` in one read transaction. Grants come from a single query, not one per role. Lists are sorted and never nil.
- `internal/pages/cloudsync_wire.go`: on the main till only, the check-in's `DeviceExtra` carries `roles: [{role, label, origin, grants}]` and `permission_actions`.
  - Built-in labels are the `users.role.*` key in the till's UI locale. If no key exists, the stored label is sent, never the raw key. Cloud labels are sent as stored.
  - `rolesReportGate` sends the report only when `sync_admin_version.generation` differs from the generation of the last check-in that got through. "Got through" means `AfterTick(contacted && err == nil)`. Any other outcome makes the next check-in re-send. The first check-in after a start always sends, an untracked generation sends every time, and a satellite resets the gate.
  - `wireCloudLinkHooks` now **chains** onto `buildCloudHooks`' `AfterTick` instead of replacing it.
- Tests: `internal/data/auth_repo_roles_report_test.go` and `internal/pages/cloud_roles_report_test.go`.
- `web/help/img/manifest.json`: surface-hash refresh only. The change is backend-only and renders nothing.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | No byte budget on the report. ut-cloud refuses a sync body over 4 MiB, and the gate re-sends until a check-in succeeds, so an oversized report (thousands of roles) would fail **every** heartbeat from then on and stop all directives. | **Fixed:** added `rolesReportByteBudget` (1 MiB, the same as `configReportGate`). An over-budget report leaves both keys out with a warning and isn't retried until the generation moves. Test: `TestCloudRolesReport_OverBudgetLeavesKeysOut`. |
| 2 | nit | `rolesReportGate.reset()` was untested. | **Fixed:** `TestCloudRolesReport_ResetResends` drives the gate directly, because the `sync.primary_url` write that demotes or promotes a till moves the admin generation by itself, which would hide a missing reset. |
| 3 | nit | A built-in role without a `users.role.*` key would report the raw key as its label. | **Fixed:** `builtinRoleLabel` falls back to the stored label, mirroring `cloud_role_directives.go`. Test: `TestBuiltinRoleLabel_MissingKeyFallsBack`. |
| 4 | note | ut-cloud doesn't ingest `roles`/`permission_actions` yet. Its decoder is lenient, so the keys are dropped, not refused, and there is no heartbeat regression. Once #3167 ships, a till that already reported won't re-send until an admin write or a restart. | Accepted. This is per ADR-0128 §5 (no periodic refresh). Noted on #3167 for the cloud rollout. |

The reviewer confirmed:
- `tick()` returns `(true, nil)` only after an accepted POST, or after a 304 whose state hash covers the full `DeviceExtra` map, so "got through" means the cloud holds this report.
- `DeviceExtra` has no caller outside `tick`.
- Reading the generation before the snapshot can only over-send.

## Verified beyond the automated tests

- Mutation checks, each confirmed failing and then passing again once restored:
  - dropping `err == nil` from `commit`;
  - letting satellites send;
  - making the gate always send;
  - replacing instead of chaining `AfterTick`;
  - disabling the byte budget;
  - emptying `reset()`.
- Full gate on the branch:
  - `gofmt`, `go build ./...` and `go test ./...` pass;
  - `golangci-lint` reports 0 issues;
  - these guards pass: core-neutral, data-access, docs-shots, i18n, kiosk-engine, no-showmodal, page-http-error, plugin-menu-read, pipefail-grep-q, migration-version-collision, compliance-claims, competitor-naming, help-topics.
- `guard-deadcode-baseline` fails locally on `internal/logging` functions this diff doesn't touch. The cause is that `cmd/unitill-desktop` is skipped without GTK headers. CI analyzes all roots.

## Verdict

Safe to merge.
