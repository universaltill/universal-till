# Review: telemetry report sends the ADR-0116 device credential (ut-docs#3561)

Date: 2026-10-03 · Lane: `lane:cloud-24` · Built by Opus 5.5, reviewed by Fable (fresh context, isolated worktree)

## What shipped
- `internal/plugins/telemetry_client.go`: `TelemetryClient` takes an
  `identity func() TelemetryIdentity` read on every tick, and sends
  `Authorization: Bearer <device credential>` on `POST /v1/telemetry/report`.
  It skips quietly until the till has an endpoint, a store id and a credential.
  A refused credential (401) is one more failed report: returned, logged by the
  scheduler, not retried.
- `internal/server/server.go`: `telemetryIdentity(cfg)` reads
  `enroll.Effective(cfg).Marketplace` per tick (store, token after pairing or
  rotation without a restart) and the device id the credential was minted for
  (`enroll.CurrentStatus().DeviceID`, falling back to `DeviceIDFromConfig`).
- `docs/data-model.md`: the telemetry job/endpoint description matched an old
  batched-events design; now describes the snapshot + bearer behaviour.

## Not in this change
- Deleting `internal/plugins/oauth`: the card makes it conditional on a real
  till round-tripping a report, and the package still has many callers
  (`internal/app`, `internal/pages/*`). Filed as a follow-up card.

## TDD
- `TestTelemetryClient_ReportNow_SendsActiveInstalledPlugins` fails without the
  header line (`Authorization = "", want the ADR-0116 device credential as a
  bearer`) and passes with it; `ReadsLiveIdentityEachTick` fails the same way.
  Re-verified independently by the reviewer in its worktree.
- New: `SurfacesRefusedCredential` (401 → error, exactly one call),
  `ReadsLiveIdentityEachTick`, the not-enrolled matrix (no store / no
  credential / no endpoint), `TestTelemetryIdentityFallsBackToConfiguredDevice`.

## Findings
| Sev | Finding | Outcome |
|---|---|---|
| minor | Device id came from `DeviceIDFromConfig`; with an env-pinned `UT_MARKETPLACE_DEVICE_ID` different from the persisted id, the credential is bound to another device → 403 `device_mismatch` every tick. cloudsync uses `enroll.CurrentStatus().DeviceID`. | Fixed (`telemetryIdentity`). |
| nit | The not-enrolled guard effectively keys on the token (StoreID defaults to the store name). | Comment clarified. |
| nit | `MerchantID` is ignored by the cloud. | Noted as wire-compat in the comment. |
| nit | Stale "stub for T024" comment on the telemetry job. | Fixed. |

Checked with no issue: races (`enroll.Effective` copies under `RLock`; `-race`
clean), no token in logs or errors, no other `NewTelemetryClient` caller,
offline-first (background goroutine only, 30s timeout), no raw SQL, no
user-facing strings, no file writes.

## Verified beyond unit tests
Driven cross-repo run: the till's real `TelemetryClient` (built from this
branch) posted to ut-cloud's real `TelemetryHandler` (its test fixture: SQLite
ent client, two provisioned stores) over HTTP at `<url>/api`:
- store A's credential → `200`, row persisted;
- a bogus token → `401`; store B's credential for store A → `401`.

Gate: `go build ./...`, `go vet`, `go test ./...` (all ok), golangci-lint
0 issues on the touched packages, CI guards. Locally `guard-deadcode-baseline`
flags `internal/logging/file.go` (untouched by this diff) and
`guard-shellcheck-version` has no shellcheck binary in this container — both
environmental/pre-existing; CI is authoritative.

## Verdict
Safe to merge.
