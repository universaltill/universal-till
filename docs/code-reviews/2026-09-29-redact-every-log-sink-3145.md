# Review: redact card numbers and credentials on every log sink (ut-docs#3145)

Date: 2026-09-29 · Lane: `lane:cloud-54` · Author: Opus 5.5 (dev subagent) · Reviewer: Fable (independent, fresh context)
Companion: ut-cloud `docs/code-reviews/2026-09-29-redact-issue-report-logs-3145.md` (server-side re-redaction + mirror guard).

## What shipped

- `internal/logging`: `logKeyf` redacts the formatted message **once, before** it enters the Problems ring and before printing. So bug-report bundles (`issuereport`) and the cloud heartbeat (`collectProblems`) only ever carry redacted text. Redaction runs after the level filter, so suppressed lines cost nothing.
- Console sinks:
  - `Init` writes the package logger through `redactingWriter{os.Stdout}`.
  - `Init` also takes over the stdlib `log` output (`redactingWriter{os.Stderr}`), but only while it is still the default `os.Stderr`, so tests or callers that redirected it keep their writer.
  - `AttachFile` redacts once and then tees to console and file.
  - `DetachFile` returns to redacted console output.
  - `Stderr()` is redacted with or without a file attached.
- `Redact` is now idempotent. An already-redacted unquoted value (`pin=[REDACTED]`) read as `[REDACTED`, and a second pass produced `[REDACTED]]`. The equality check is exact, so a value that merely *starts* with the marker is still redacted.
- Belt and braces:
  - `issuereport.saveBundleFiles` re-redacts `Meta.Logs`.
  - `cloudsync.uploadIssueReport` redacts at upload, which covers bundles saved by an older version.
- `internal/db`: the two migration re-stamp/re-apply **warnings** now log a 12-hex checksum prefix. A full 64-hex checksum reads as a token and would arrive as `[REDACTED]` on every sink. The returned ADR-0074 *error* keeps both full checksums (tests pin that).
- `CaptureForTest` stays raw on purpose. It proves a secret is never formatted into a log call at the source.

## Tests (each seen failing before the fix)

The secrets used are the PAN `4111 1111 1111 1111`, a Bearer token and a JWT.

- `TestConsoleSinksAreRedacted`: a subprocess run in three modes (no file, attached, detached), covering stdout, stdlib log on stderr, and `Stderr()`.
- `TestRecentHoldsRedactedMessage` (ring), `TestRedactIsIdempotent`.
- `TestSaveWritesRedactedLogsToMeta` (bundle), `TestUploadIssueReportRedactsLogs` (upload), `TestCollectProblems_RedactsSecrets` (heartbeat payload).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | minor | Migration re-stamp warning lost its checksums on every sink, and the dev had dropped the test's checksum assertion. | Fixed: log a 12-hex prefix (`checksumPrefix`). The assertion is restored on the prefix. |
| M2 | minor | `wasm_runtime_ask_log_integration_test.go`: the `"omitted" \|\| [REDACTED]` disjunction had a dead first branch. | Fixed: assert `"auth_token":[REDACTED]` only. The placeholder itself stays pinned by `wasm_runtime_ask_log_test.go`. |
| M3 | minor | L() lines are redacted twice (~75 µs/line each, after the level filter). | Accepted: under 1% CPU at till volumes, even on a Pi. Could be optimised later. |
| M4 | minor | Reverting `bundle.go` alone leaves the bundle test green, because the ring is already redacted. | Accepted: belt-and-braces by design. The test proves the end-to-end path. |
| N1 | nit | The console-sink child process inherited `UT_LOG_LEVEL`. | Fixed: the child pins `UT_LOG_LEVEL=info`. |
| N2 | nit | `DetachFile` resets stdlib log unconditionally. | Accepted: pre-existing behaviour. |

Remaining unredacted paths (reported, not widened; low risk): unrecovered goroutine panics write straight to fd 2, and `internal/plugins/oauth/token_client.go` does a direct `fmt.Fprintf(os.Stderr, …)` of an OS error. Both are filed as a Backlog card.

## Verified beyond automated tests

The reviewer re-ran each TDD claim: it reverted `logging.go`, `redact.go`, `cloudsync/issue_reports.go` and `bundle.go` one at a time, saw each test fail, restored the file and saw it pass. `go test -race` on logging, issuereport and cloudsync is clean. Redact was benchmarked at 70–80 µs/line. No UI surface changed, apart from the back-office "recent problems" panel, which now shows redacted text; that is the intended behaviour. The help prose ("tokens are removed before anything is written") is still accurate.

## Verdict

Safe to merge. **Merge this before the ut-cloud PR**: ut-cloud's mirror guard checks POS `main`.
