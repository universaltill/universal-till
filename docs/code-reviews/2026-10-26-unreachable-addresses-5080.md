# Review: fixed refused address for "unreachable" test targets (ut-docs#5080)

Scope: test-only change (follow-up of ut-docs#5019 / #4996). No product code changed. `internal/pages` sites
outside the three files below are left to open PR #2166, which owns the helper for that package.

## Root cause
Tests made an "unreachable" address by binding `127.0.0.1:0` (or an `httptest` server) and closing it. A later
listener in the same test binary can be handed that port, so the address is not reliably dead. `http://127.0.0.1:0`
used as a target is not a dialable dead address either.

## Fix
Use `127.0.0.1:1` (refused at once) with a one-line why-comment per site: netreach, lantls pin, tax-tr okc bridge,
plugins (wasm tcp, okc, installer/telemetry placeholders), updates, lookup, alerts, cloudsync startup check-in,
marketplace client, pages/catalog lookup, and the three named `internal/pages` files. Sites that need a FREE
port to bind later (server, recovery, app, plugin IPC) are left, with a comment saying why. The asserted error
class of every test is unchanged.

## Independent review (Opus subagent, different model)
Traced each site into the code under test: no port/loopback validation turns port 1 into a vacuous pass.
Mutation-checked 12 changed tests against a live target; all 12 fail as they should. Findings fixed here: a missed
dead-address site (`internal/plugins/okc_plugin_test.go`), the `:0` placeholders in `internal/plugins`, a stale
comment (`wasm_tcp_test.go`) and the missing "needs a FREE port" comment (`ipc_integration_test.go`).
Pre-existing, not changed: okc `ErrDeviceUnreachable` also covers read timeouts.

## Verification
`gofmt` clean, `go vet` on changed packages, `golangci-lint` 0 issues, changed tests pass with `-race`.
Full `go test -race ./internal/plugins` exceeds 40 min on this runner (also at baseline); CI runs it.
Local guards pass except `guard-deadcode-baseline` (runner's `deadcode` binary built with go1.26, repo needs 1.27)
and `guard-shellcheck-version` (no shellcheck binary): both environmental.
