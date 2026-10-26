# Review: deadPrimaryURL can never be answered (ut-docs#4996)

**Change:** `internal/pages/held_sale_sync_proxy_test.go`,
`internal/pages/dead_primary_url_4996_test.go` (new). Test-only.

## What shipped

- `deadPrimaryURL()` returned the URL of an httptest server it had just
  closed. The kernel reuses closed ephemeral ports (the cause of #4985), so a
  later server in the package could be listening there and the ~30
  "primary unreachable" tests would get an HTTP answer instead of a
  connection error. It now returns `http://127.0.0.1:1`. Port 1 is privileged
  and below every ephemeral range, so httptest (port 0) is never handed it,
  and a dial is refused at once. That is the same error class as before.
  The signature is unchanged, so no call site changed.
- `TestDeadPrimaryURLStaysDead` does three things:
  - it starts 20 servers and checks none of them gets the dead address;
  - on Linux, it asserts the dead port is below
    `/proc/sys/net/ipv4/ip_local_port_range`'s low bound;
  - it asserts a dial afterwards fails with `ECONNREFUSED`.

## Review

Independent review by a different model from the author (Sonnet built it,
Opus reviewed it in a fresh context, in a separate worktree).

- **Call sites:** every one only stores the URL as `sync.primary_url`.
  Nothing compares it, expects a unique port, or validates the port.
  `SyncTarget` only trims it.
- **Loopback checks:** they see `127.0.0.1` exactly as before.
- **TDD re-verified by the reviewer:** with the old body the new test fails
  with `dead port 41511 is inside the ephemeral range starting at 32768`.
  With the fix it passes, 5/5 runs.

Findings:

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The same closed-server pattern is still in `auto_register_test.go`, `setup_base_plugins_test.go`, `setup_language_catalog_test.go`, `setup_tax_catalog_test.go` and `setup_tse_test.go`. | Out of the card's scope. Filed as ut-docs#5019. |
| 2 | nit | The 20-server loop can't fail with port 1. The decisive check (ephemeral range) runs only on Linux. | Accepted: CI is ubuntu-latest, and the loop documents the intent. |
| 3 | nit | `ECONNREFUSED`/2s dial is not Windows-portable. | Accepted: `internal/pages` tests don't run on Windows; the Windows job runs `./cmd/unitill-desktop` only. |

## Verified

- `gofmt -l` clean, `go build ./...`, `go vet ./internal/pages/`.
- `golangci-lint run ./internal/pages/...`: 0 issues.
- Full `go test ./internal/pages/ -count=1`: ok (≈325 s), run by both the
  author and the reviewer.
- The ci.yml `build` guards were run locally: all pass. Three scripts could
  not run here: `guard-shellcheck-version` has no shellcheck binary in the
  container, and the release helpers `prune-nightly-assets` and
  `retry-with-backoff` need different inputs. No shell scripts changed.
- No UI, no help topic, no locale keys.

**Verdict:** safe to merge.
