# Code review: report-only Content-Security-Policy + `/csp-report` sink (ut-docs#2913, slice 1)

- **Date:** 2026-10-01
- **Lane:** `lane:cloud-24`
- **Branch:** `feat/2913-csp-report-only`
- **Built by:** Opus 5.5 (dev subagent)
- **Reviewed by:** Fable (independent subagent, fresh worktree)
- **Fixes and re-verification:** orchestrator (Opus 5.5)

## What shipped
- **`UT_CSP_REPORT_ONLY`** (`internal/config`). Default off; an unparseable value counts as off.
- **Header.** With the flag on, `cspReportOnlyMiddleware` sets `Content-Security-Policy-Report-Only: default-src 'self'; script-src 'self' 'report-sample'; object-src 'none'; base-uri 'none'; frame-ancestors 'self'; report-uri /csp-report` on every response.
  - It is the outermost wrapper inside `recoverMiddleware`, on both the auth-on and auth-off branches.
  - It never wraps the `ResponseWriter`, so SSE and Hijack keep working.
- **`POST /csp-report`** (`internal/pages/csp_report.go`).
  - **Formats:** `application/csp-report` and `application/reports+json`.
  - **Limits and errors:** body cap 64 KiB (413); 400 for a malformed report or one with no directive; 415 for other types; 405 from the mux for other methods.
  - **Sanitising:** fields have control characters removed and are capped at 256 runes. The query and fragment are dropped from every URL.
  - **Storage:** violations are deduplicated into a bounded in-memory set of 1000.
  - **Logging:** each new violation is logged once at **INFO**.
- **`GET /csp-report`** returns the inventory as `{data, error}`. It sits behind the session and the `settings` permission.
- **Auth exemption.** `auth.exemptRequest` exempts only an exact `POST /csp-report`, and only after `pages.Init` calls `authSvc.EnableCSPReportSink()`. Init does that only when the flag is on, so with the flag off the chain behaves exactly as before.
- **Demo mode** denies both routes.
- **Guard skip lists.** `/csp-report` is skipped in the help-route-coverage and page-http-error guard lists, because it is JSON, not a page.
- **e2e.** A dedicated `csp` project (`run-till-csp.sh`, port 8098) and `csp-report-only-2913.spec.ts`. Worker tills pin `UT_CSP_REPORT_ONLY=0`.
- **README** has a section on the flag.

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| **Blocker** | The auth exemption was unconditional while the route existed only with the flag on. With the flag off, an anonymous `POST /csp-report` skipped auth and reached the `/` catch-all, which could render a plugin page registered at that route. That broke "flag off changes nothing". | **Fixed.** The `Service.EnableCSPReportSink` seam is set only when the flag is on. New test `TestCSPReportExemptionOffWithoutSink`, and `TestInit_CSPReportOnlyFlag` (flag off) now asserts auth's own 303/401. Re-verified: with the seam check removed, the new test fails ("reached the handler anonymously … status 200"); restored, it passes. |
| Should-fix | `TestCSPReport_Sanitises` counted every line in a process-global log capture, so it was flaky. | **Fixed:** it counts `csp-report:` lines only. |
| Should-fix | Per-violation lines were logged at WARN. The Problems ring (50 lines) feeds bug reports and the cloud heartbeat, and one page load yields more than 50 unique violations. Because the sink is anonymous, a LAN client could also flush the ring. | **Fixed:** per-violation lines are INFO. Only the one-time "cap reached" line stays WARN. |
| Nit | A report with no directive was stored. | **Fixed:** 400 (test case "no directive"). |
| Nit | Any session could read the inventory. | **Fixed:** the `settings` permission gates it (`TestCSPReport_InventoryNeedsReadPermission`). |
| Nit | The concurrency test called `t.Fatalf` off the test goroutine. | **Fixed:** handlers are called directly. |
| Nit | Mux 405s are plain text, not the envelope. | **Accepted:** this is the mux's standard behaviour, the same as every method-pattern route in the till. |
| Nit | The `reports+json` parser is unused while the policy has only `report-uri`. | **Accepted:** it is cheap and ready for `report-to`. |
| Nit | `int` counters could wrap on 32-bit builds. | **Accepted:** unreachable in practice. |

## Verified beyond unit tests
- **TDD claims re-run by the orchestrator in a separate worktree.** Removing `cspWrap` from the auth-on branch fails `TestInit_CSPReportOnlyFlag` (empty header on `/healthz`, `/login`, `/` and `/settings`). Restoring it makes the test pass.
- **Real e2e run, `npx playwright test --project=csp`:** 1 passed.
  - All 13 surfaces carry the exact header, and so does `/healthz`.
  - The inventory holds 303 unique violations: `style-src-attr` 199, `script-src-elem` 86, `style-src-elem` 14 (htmx's injected style), and `script-src` 4 (`eval` in alpine/htmx).
  - This inventory is the worklist for #3325/#3326, and the eval and style findings feed #3327.
- **Gate:**
  - `gofmt` is clean, `go vet ./...` is clean, `golangci-lint` reports 0 issues, and `go test ./...` passes in full. `-race` passes on config, auth and pages (CSP/Demo/Recover).
  - All 38 CI guard scripts pass except two that fail for environment reasons:
    - `guard-shellcheck-version` fails because there is no shellcheck binary in the container.
    - `guard-deadcode-baseline` reports `logging.Stderr` and `timestampWriter.Write`. It reports the same two functions with this change stashed, because the container has no GTK headers and the guard skips `cmd/unitill-desktop`; CI analyses it.
  - A test-only method was moved into the test file to keep the deadcode guard clean.
  - The docs-shots surface hash was refreshed. The change is behind a flag that is off by default and renders no pixel (`Docs-Shots-Unchanged: true`).

## Not in this slice
- #3325: move `hx-on` and inline `on*=` handlers into script files.
- #3326: per-request nonce plumbing for inline scripts.
- #3327: enforce the policy and check the devices (`blocked:dep` on the two above).
- The `e2e/` projects are not run by CI's `e2e` job (`tests/e2e`). The `csp` project is run locally, the same as the other dedicated projects.

## Verdict
Safe to merge.
