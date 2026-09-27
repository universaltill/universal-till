# 2026-09-27 — Log file redaction gaps + stdout-only prints (ut-docs#2728)

## What shipped
- **`internal/logging/redact.go`** — the free-text redactor for `till.log` (#2720) now also removes:
  - PIN values under any `_`/`-`-joined key: `admin_pin=`, `user_pin=`, `manager-pin=`, `pin_code=`, `pin_hash=`, quoted JSON keys too. `\bpin` alone missed `admin_pin` because `_` is a word character.
  - `otp=`, `totp=`, `hotp=`, `otp_code=`.
  - `session=`, `ut_session=`, `session_id=`, `sessionid=`. `sessions=3` and `session_count=3` stay.
  - OAuth `?code=` / `&code=`, and the named one-time codes `auth_code`, `authorization_code`, `pairing_code`, `redeem_code`, `device_code`, `user_code`, `verification_code`, `recovery_code`, `reset_code`. Bare `code` stays: `status code=404`, `exit code=1`, `country_code=DE`, `currency_code=EUR`.
  - **Card numbers (PANs)**: any group-aligned span of 13–19 digits, contiguous or joined by one consistent space/dash, whose first digit is 2–6 (a real IIN; a Unix-ms timestamp leads with 1) and that passes Luhn.
- **stdout-only prints → `logging.L()`**: 25 `fmt.Printf`/`Println` calls in `internal/plugins/{supervisor,permissions,ipc,install}.go` and `internal/app/provision.go`. On a Windows GUI launch stdout is an invalid handle, so these never reached `till.log`. `warning:` lines are Warn; the summary lines are Info. `Failed to start: [...]` is Info because each failure already logged its own Warn, and a second one would double-count it in the problems ring.
- **Guard**: `internal/logging/no_stdout_print_test.go` walks `internal/` with `go/ast` (skipping `testdata/` and `_test.go`) and fails on any `fmt.Print`/`Printf`/`Println`.
- `AttachFile` doc note: the log file is attached before the data-dir lock (#1097), so a second instance that is about to be refused briefly appends to the same `till.log`. This is harmless.

## Review
Built by Sonnet 5 (easy card). Independent reviewer: Opus 5.5 in a separate worktree. Verdict: safe to merge once finding 1 was fixed (it is fixed).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The PAN candidate regex was greedy, and a failing candidate was skipped as a whole. A card glued to a neighbouring number with the same separator survived: `tender 2 4111111111111111`, `table 12 4111 1111 1111 1111`, `4111111111111111 5`, `ids 1234-5678 4111 1111 1111 1111`. | **Fixed:** `panRunRe` finds whole digit-group runs, and `panSpans` tries every group-aligned sub-span of 13–19 digits (at most 19 per start, so linear time). Spans never cut a prefix out of a longer contiguous run. 8 new secret cases and 2 keep-cases (`41111111111111110000`, a Luhn-invalid 16-digit run). |
| 2 | minor | Any pin-suffixed key is a secret, so `pin_count=3`, `pin_attempts=3` and similar lose their value. | Accepted: no such key is logged in `internal/` today, and the design errs toward removing. |
| 3 | nit | `session_id` is a `sessions.id` row UUID here, not a credential (the credential is the `ut_session` cookie). | Accepted: nothing logs it today; losing a correlation id is the cheaper error. |
| 4 | nit | `Failed to start` at Warn double-counts in the problems ring. | **Fixed:** now Info. |
| 5 | nit | The guard matches only the identifier `fmt`. An aliased import, `fmt.Fprint*(os.Stdout, …)` and the built-in `println` get past it. | Accepted: covers the shape that actually occurred. |
| 6 | ok | `provision.go`'s only caller (`packaging/scripts/postinstall.sh`) checks the exit code and never parses stdout. The logger still writes to stdout. | — |

TDD re-verified by the reviewer:
- The pre-change `redact.go` with the new tests fails `TestRedactRemovesSecrets` with 32 `secret survived redaction` lines (`admin_pin=1234`, `otp=778899`, `?code=…`).
- The WIP redactor with the reviewer's tests fails on `amount 1 378282246310005` and `ids 1234-5678 4111 1111 1111 1111`.
- Reverting the plugin/app conversions makes `TestNoStdoutPrintCalls` fail, listing all 25 sites.
- Everything is green after the fixes.

## Verified
- `gofmt -l .` (clean), `go build ./...`, `go vet` (logging, plugins, app), full `go test ./...`, `golangci-lint run ./...` (0 issues).
- Guards: data-access, pipefail-grep-q, price-history-sync, migration-version-collision, kiosk-engine, demo-env, plugin-menu-read, page-http-error, plugin-settings-bump, i18n, compliance-claims, competitor-naming, core-neutral.
- Backend only: no UI, no locale keys, no help topic affected.

## Deferred
- Findings 2, 3 and 5 are accepted as above. None warrants a card on its own.
