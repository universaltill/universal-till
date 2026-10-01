# Review: goroutine panic recovery + stdout-guard bypass shapes (ut-docs#3304)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54
- **Author model:** Opus 5.5 (Dev subagent). **Reviewer model:** Fable (independent, fresh context).
- **Branch:** `feat/3304-goroutine-recover-guard`

## What shipped

1. **`logging.RecoverAndLog(name)`** (`internal/logging/recover.go`). It is used as
   `defer logging.RecoverAndLog("…")` at the top of a goroutine. It recovers a
   panic and logs the value plus `debug.Stack()` through `L().Errorf`, so the
   output is redacted and reaches till.log and the Problems ring. The goroutine
   then ends. This follows the existing rule that a background goroutine never
   takes the till down mid-sale (`pluginUpdateCheckTick`); until now the Go
   runtime wrote such a panic to fd 2 unredacted.
2. **All 64 `go` statements** in non-test `internal/` code are wrapped (44 files).
   A direct call `go x.f(args)` became `go func(p){ defer …; x.f(p) }(args)`,
   so arguments are still evaluated at the `go` statement.
3. **Goroutine guard** (`internal/logging/no_unrecovered_goroutine_test.go`).
   Every `go` statement must be a func literal whose first statement is
   `defer <logging>.RecoverAndLog(…)`, with `<logging>` resolved through the
   file's imports, or carry `// goroutine-recover:allow <reason>`. The
   planted-case test covers the bad, ok and reason-less-allow shapes.
4. **Stdout guard** (`no_stdout_print_test.go`):
   - import names are resolved per file, so an aliased `fmt`/`os` is caught;
   - a dot-import of `fmt`/`os` is a violation;
   - **any** `os.Stderr`/`os.Stdout` reference outside the `internal/logging`
     directory is a violation (`.Write`, `.WriteString`, `io.WriteString`,
     `w := os.Stderr`);
   - unresolved `print`/`println` builtin calls are violations.

   Every shape is planted in the test.
5. **Sites where continuing after a recovered panic needed care.** The result
   or state write is now deferred so a panic cannot leave a caller waiting
   forever or a flag stuck:
   - `discovery/printers.go`: the browse result is deferred, defaulting to `errBrowsePanicked`.
   - `discovery/browse.go`: `close(entries)` and the `queryErr` send are deferred.
   - `netreach.probe`: the state and `inFlight=false` write is deferred.
   - `procrestart`: `close(done)` is deferred.
   - `selfupdate/{pending,wininstaller}.go`: `close(done)` is deferred.
6. `web/help/img/manifest.json`: the surface hash was refreshed because
   `internal/pages/*.go` changed. Nothing renders differently (`guard-docs-shots.sh`).

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The goroutine guard exempted bare `RecoverAndLog` by **package name**, so another package named `logging` with a no-op `RecoverAndLog` passed. | **Fixed:** the exemption is keyed on the directory (`filepath.Dir(rel) == "logging"`), the same rule the stdout guard uses. Added the planted `foo/logging/impostor.go` case; it failed before the fix. |
| 2 | should-fix | No test covered the recovered-panic paths of the semantic rewrites. | **Fixed for the two unbounded ones:** `TestPanickingProbeStillClearsInFlight` (netreach) and `TestBrowse_PanickingQueryReturnsErrorPromptly` (discovery). Both were confirmed to **fail** with the pre-fix (non-deferred) code (`in flight true`; `still blocked 5s`) and pass with it. The procrestart and selfupdate waits are bounded by `hookBound` and accepted without a seam test. |
| 3 | nit | `bluetooth/android_bridge.go`, `print/transport.go` and `recovery/serve.go` send their result non-deferred, so after a recovered panic the caller waits for its ctx timeout. | Accepted: every caller's wait is bounded by ctx/timeout. |
| 4 | nit | selfupdate hooks: `close(done)` was not deferred, unlike procrestart. | **Fixed** (deferred). |
| 5 | nit | `wasm_runtime` drainer: after a recovered panic the plugin's queued events are dropped until the next Sync (sends are non-blocking). | Accepted. Checkout never blocks; before this change the till crashed. |
| 6 | nit | The "Close entries HERE" comment in browse.go was left trailing. | **Fixed** (moved onto the deferred close). |
| 7 | nit | A recovered-panic Problem carries a multi-KB stack into the back-office Problems table. The heartbeat truncates to 200 runes. | Accepted (cosmetic). The ring is capped at 50. |
| 8 | out of scope | `cmd/unitill-desktop` has 5 bare `go` sites outside `internal/`. | Follow-up card filed. |

The reviewer also checked and found OK:
- the defer ordering (RecoverAndLog is registered first, so the existing `wg.Done`/`close`/unlock defers still run);
- `recover()` is called directly by the deferred function;
- no logger mutex is held across user code;
- build-tagged other-OS files are parsed;
- generics, method values, a foreign `logging` alias and a wrapping `defer func(){ logging.RecoverAndLog() }()` are all rejected.

## Verified beyond the automated tests

- **TDD, re-verified by the reviewer in a separate worktree:**
  - with `RecoverAndLog` made a no-op, the recover test crashes;
  - with the netreach wrapper removed, the goroutine guard names `netreach/netreach.go:167`;
  - an added `os.Stderr.WriteString` plus an aliased `f.Println` are named by the stdout guard.
- **Gate after the review fixes:**
  - `gofmt -l` prints nothing; `go build ./...`, `GOOS=windows go build ./...`, `GOOS=darwin go build ./...` and `go vet ./internal/...` all pass.
  - Full `go test ./...` has no failures, and `golangci-lint run ./...` reports 0 issues.
  - Every `ci.yml` build-job guard passes except two that fail here for environment reasons only:
    - `guard-shellcheck-version.sh`: no shellcheck in the container.
    - `guard-deadcode-baseline.sh`: also fails on the unmodified baseline here; `cmd/unitill-desktop` cannot compile without GTK headers.

  CI is authoritative for both.
- There is no UI surface, so there is no UX or visual check, and no help topic changes.

## Verdict

Safe to merge.
