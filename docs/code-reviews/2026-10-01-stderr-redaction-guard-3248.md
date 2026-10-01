# Review: stderr writes go through the redacting logger (ut-docs#3248)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54
- **Author:** Opus 5.5. **Reviewer:** Fable (independent, fresh context, in a separate worktree)
- **Card:** universaltill/ut-docs#3248 (found in the #3145 review, `2026-09-29-redact-every-log-sink-3145.md`)

## What shipped

- `internal/plugins/oauth/token_client.go`: the "failed to cache token to
  disk" warning now goes through `logging.L().Warnf`, not
  `fmt.Fprintf(os.Stderr, …)`. It is redacted and lands in `till.log`.
- `internal/logging/no_stdout_print_test.go`: the AST guard
  (`TestNoStdoutPrintCalls`, first added for #2728) still flags
  `fmt.Print/Printf/Println`. It now also flags
  `fmt.Fprint/Fprintf/Fprintln` whose writer is `os.Stderr` or `os.Stdout`,
  anywhere under `internal/` except `testdata/` and `_test.go` files. The
  walk moved into `findStdoutWrites(root)`.
  `TestFindStdoutWritesCatchesPlantedCalls` plants each forbidden shape in a
  temp tree and checks that every one is reported. It also checks that
  `Fprintf(&buf)`, `Fprintln(w)`, comments, string literals, a test file and
  a `testdata/` file are not reported.

## TDD evidence

- Before the fix, the widened guard failed on `main` with the real
  violation: `plugins/oauth/token_client.go:102`.
- The reviewer re-verified this independently. Reverting token_client.go
  makes `TestNoStdoutPrintCalls` fail on that line, and restoring it makes
  the test pass. Removing the `Fprint*` case from the guard makes
  `TestFindStdoutWritesCatchesPlantedCalls` fail (`[bad/stderr.go:12]`
  instead of lines 9–12).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | `fmt`/`os` are matched by identifier name, so a shadowing local var could false-trigger and a dot-import would be a false negative. This was already true before this change. | Accepted. It's contrived, and type-checking the tree isn't worth it. |
| 2 | follow-up | Other direct writes are not covered: `os.Stderr.Write/WriteString`, `io.WriteString(os.Stderr, …)`, the `print`/`println` builtins, `w := os.Stderr; fmt.Fprintf(w, …)`, an aliased `os` import. None exist in the tree today (grep-verified). | Out of scope for this card. Filed as a Backlog follow-up, together with the goroutine-panic part of the card title. |
| 3 | follow-up (docs) | `reference/coding-standards.md` doesn't mention this guard. The #2728 record describes the narrower scope. | This record states the widened scope; historical records are left as they are. |

The reviewer also checked these:
- `logging.L()` is nil-safe (lazy `Init`), and there is no import cycle (`logging` imports only `clock`).
- `saveToDisk` errors are path errors, never the token, and `logKeyf` redacts again anyway.
- Expected paths use `filepath.Join`, so the test is Windows-safe.
- The type assertions are guarded.
- The cache path is `paths.Plugins(...)`, and its directory is created with `os.MkdirAll`.

## Gate

- `gofmt -l`: clean. `go build ./...` passes. `go vet` on the touched packages passes. `golangci-lint` on the touched packages reports 0 issues (reviewer's run).
- `go test ./...`: all packages pass except one load-sensitive failure in an
  untouched package, `internal/selfupdate`
  `TestWatchdogQuietWhileASaleOpenedDuringTheDelayHoldsTheRestart`. It
  passes 5/5 when run on its own and is tracked as a separate card.
- `ci.yml` build-job guards: all pass locally except two that this container
  can't run:
  - `guard-shellcheck-version.sh`: no shellcheck binary.
  - `guard-deadcode-baseline.sh`: no GTK headers, so `cmd/unitill-desktop`
    is skipped and `logging.Stderr` / `timestampWriter.Write` look
    unreachable. They are used by the desktop shell, and real CI analyses
    that root.
- No UI surface, no locale keys, no help-manual change (nothing a shop
  owner sees).

## Verdict

Safe to merge.

## Deferred

- The guard's wider bypass shapes (finding 2) and unrecovered goroutine panics.
  The runtime writes the panic straight to fd 2, so no writer can redact it,
  and every `go` statement needs auditing for a `recover`. Both are on one
  follow-up Backlog card.
