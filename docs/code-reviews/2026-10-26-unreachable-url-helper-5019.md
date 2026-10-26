# Review: shared unreachable URL for internal/pages tests (ut-docs#5019)

Date: 2026-10-26 · Lane: lane:cloud-24 · Complexity: easy · Test-only change

## What shipped

- New `internal/pages/unreachable_test.go` with `unreachableAddr()`
  (`127.0.0.1:1`) and `unreachableURL()` (`http://` + addr). Port 1 is
  privileged and unused, so a dial is refused at once: no timeout, and no
  reuse race. A closed httptest server's port can be handed to a later
  server in the same package (ut-docs#4996).
- `deadPrimaryURL()` delegates to `unreachableURL()`.
  `TestDeadPrimaryURLStaysDead` still pins the address.
- The "start an httptest server, close it, use its URL" pattern is replaced
  in the 10 sites listed on the card:
  - `auto_register_test.go`
  - `setup_base_plugins_test.go` ×3
  - `setup_language_catalog_test.go` ×2
  - `setup_tax_catalog_test.go` ×2, including the `deadMarketplace` helper
  - `setup_tse_test.go` ×2
- The same pattern is also replaced in 7 sites the card didn't list but
  that are in `internal/pages` scope:
  - `voucher_sync_proxy_test.go` ×3
  - `tables_claim_proxy_test.go` ×2
  - `vouchers_adjust_test.go`
  - `sync_secrets_key_test.go`
  - `sync_assets_prune_2785_test.go`
  - `catalog/item_replica_gate_test.go`. This is a subpackage, so it can't
    see `unreachableURL()` and uses an inline `http://127.0.0.1:1` with a
    comment.
- Closed-`net.Listener` `host:port` sites now use `unreachableAddr()`:
  `cloudsync_print_report_test.go` and `self_order_counter_held_test.go`.
- Left alone: `catalog_write_through_test.go:~72`. It closes the default
  listener only to swap in the caller's listener, so it isn't an
  unreachable address.

## Review

The change was built by Sonnet and reviewed by Opus 5.5 (a different model,
per MODEL-ROUTING).

- Every replaced site still asserts the same outcome: connection refused,
  then the offline, fallback or refusal path. No assertion changed.
- `sync_assets_prune_2785_test.go` now passes `&http.Client{}` instead of
  the closed server's client. A refused dial returns at once, so the missing
  client timeout can't hang the test.
- The printer tests dial a raw `host:port`. Port 1 is refused straight away,
  same as the closed listener, and the `print failed` assertion is
  unchanged.
- Finding (nit, fixed): reworded comments ran past ~80 columns. Rewrapped.
- No production code changed. There is no file write, path, money, i18n or
  UI surface, so the `MkdirAll`/`paths.Data` checks don't apply.
- TDD: there is no new failing test to re-verify. The defect is a
  nondeterministic port-reuse race. The existing pin,
  `TestDeadPrimaryURLStaysDead`, covers the shared address.

## Verified

- `gofmt -l internal/`: no output.
- `go build ./...` and `go vet ./internal/pages/...`: pass.
- `golangci-lint run ./internal/pages/...`: 0 issues.
- `go test -race -count=1 ./internal/pages/...`: see the PR for the result.
- Every guard in `ci.yml` was run locally. Two can't run in this container:
  - `guard-deadcode-baseline` fails identically on `main`: the deadcode
    binary was built with go1.26 and the repo needs go1.27.
  - `guard-shellcheck-version`: no `shellcheck` binary is installed.
  - CI runs both.

## Deferred

The same closed-listener pattern exists outside `internal/pages`:
`internal/netreach`, `internal/lantls`, `internal/app/recovery_mode_test.go`
and `plugins/tax-tr/okc`. Separately, some tests use port `0` as a "dead"
target. Both are filed as ut-docs#5080.

**Verdict: safe to merge.**
