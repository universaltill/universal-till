# 2026-07-24 — Wire the plugin Supervisor into boot and shutdown (T023)

## Context
`ut-docs/QUEUE.md` gap: `Supervisor.AutoStartPlugins()`
(`internal/plugins/supervisor.go:292`, T023) was fully implemented and unit
tested in isolation, but never constructed or called in production —
`main.go` passed `nil` straight through to `server.Start` with a
`// TODO: Initialize supervisor` comment. Two consequences, both silent
(nil-guarded, so nothing crashed — it just never worked):
1. A process-based (hardware) plugin left active from a previous run would
   never restart on boot.
2. `internal/plugins/revocation.go`'s `RevocationChecker.processRevocation`
   calls `rc.supervisor.StopPlugin(...)` but only `if rc.supervisor != nil`
   — revoking a process-based plugin never actually stopped its process.

Zero observable effect today: every currently-shipped plugin is WASM, and
`AutoStartPlugins` explicitly skips any runtime other than `"go"`/`"native"`.
This closes the gap so it's ready before the first process-based plugin
ships, rather than being rediscovered then.

## Design
- `main.go`: construct `supervisor := plugins.NewSupervisor(database.DB)`
  and call `supervisor.AutoStartPlugins(ctx)` (non-fatal — logs a warning
  and continues booting on error, matching this repo's offline-first
  "checkout must never be blocked" philosophy), then pass the real
  `supervisor` (not `nil`) into `server.Start`.
- `internal/server/server.go`: the existing graceful-shutdown goroutine
  (`<-ctx.Done()` → `srv.Shutdown`) now also calls
  `supervisor.Shutdown(shutdownCtx)` when non-nil, so a running process
  plugin doesn't outlive the till.
- `internal/plugins/supervisor_test.go`: new `TestSupervisor_AutoStartPlugins`
  — seeds a native-runtime active plugin, a wasm-runtime active plugin, and
  an inactive go-runtime plugin; asserts only the native one actually
  starts (real subprocess, checked via `IsRunning`/`ListRunning`).

## Independent review
Opus-model review, adversarial brief (verify independently, don't trust the
implementer's summary).

**Confirmed correct (reviewer verified independently):**
- Construction ordering is safe: `NewSupervisor(database.DB)` runs after
  `db.Open` (migrations), replica-identity, and `plugins.Init` — no
  pre-migration/missing-table window.
- Non-fatal error handling is the right call: `AutoStartPlugins` only
  returns an error on the `ListAutoStartPlugins` query failing; individual
  `StartPlugin` failures are swallowed and logged per-plugin.
- `Supervisor.Shutdown`'s process-kill path (`proc.cancel()`) doesn't
  actually depend on the passed context being unexpired — worst case if the
  HTTP server's 5s shutdown budget is fully consumed first, only the
  `plugin_shutdown` audit-log write is skipped (logged as a warning);
  processes still die.
- Zero behavior change for the current WASM fleet — WASM rows are fetched
  but skipped before any `StartPlugin`/audit side effect; verified by
  reading the runtime filter directly, not just trusting the claim.
- The new test spawns a real subprocess and reads real process state, not
  just a DB row — not vacuous.
- `go build ./...`, `go test ./...`, `go vet ./...`,
  `scripts/ci/guard-data-access.sh` all re-run independently and green.

**Fixed:**
- **LOW — orphan-process window**: the first pass constructed the
  supervisor and called `AutoStartPlugins` before the marketplace
  catalog-repository init step, which has its own `log.Fatalf` on error.
  `os.Exit` (what `log.Fatalf` calls) runs no deferred functions, so a
  `Fatalf` there would exit without ever reaching `server.Start`'s shutdown
  wiring — orphaning any process a future go/native plugin had already
  started. Purely theoretical today (no such plugin exists), but free to
  close: moved both calls to immediately before `server.Start`, after every
  other `log.Fatalf`-capable init step has already succeeded. Re-verified
  with a live boot smoke test (`go build` + run against a scratch SQLite db)
  that the "Auto-started 0 plugins" log line now appears in the new,
  later position and the server still serves `200`/redirects normally.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...`,
`bash scripts/ci/guard-data-access.sh` — all green, both by me and
independently by the reviewer. Live boot smoke test run twice (before and
after the ordering fix) against a real built binary and scratch SQLite DB —
clean startup, correct log ordering, HTTP server responds.
