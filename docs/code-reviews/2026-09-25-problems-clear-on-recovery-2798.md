# Code review — problems digest clears recovered and self-healed problems (ut-docs#2798)

Date: 2026-09-25 · Branch `fix/2798-problems-clear-on-recovery` · Author: Opus 5.5 (pipeline lane cloud-54) · Reviewer: Fable (independent subagent, different model)

## What shipped

The cloud's Tills page showed "⚠ Attention needed" for healthy tills. The till's heartbeat `problems` digest (`collectProblems`) was every WARN/ERROR line still in the in-memory ring. That included a main-till outage that had long recovered, a refused proof from before re-discovery, and two successful self-repairs logged at WARN.

- `internal/logging`: `Problem` gains `Key` and `Resolved`. New `Logger.WarnProblemf(key, …)` and `ResolveProblems(key)`. `OpenProblems(now, maxAge)` returns problems that are unresolved and, if unkeyed, no older than maxAge. Keyed problems never age out. `Recent()` still returns every line, for bug-report bundles.
- The main-till outage WARN ("main till unreachable") and the refused-proof WARN are keyed `discovery.MainTillProblemKey`. `primaryContactOK` (successful admin pull, link hello) resolves them.
- The heartbeat reports only `OpenProblems` with a 24h age-out for unkeyed lines. The back-office panel hides resolved problems and has no age cut.
- The replica identity re-mint (#2730) and the by-design satellite-row prune (#1667) now log at INFO. The failed-repair path stays at WARN.
- Help `web/help/en/multitill.md` says when problems clear. The docs-shots manifest was refreshed (hashes only; PNG churn from the container's renderer was not committed).

## Findings and triage

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | major | Age-out dropped a once-per-outage WARN after 24h, so a main till down for more than a day would show as healthy | **Fixed**: keyed problems never age out, only resolution clears them. Test extended (red without the fix). Help wording updated |
| m1 | minor | The refused-proof WARN shares the outage key, so a successful contact clears it even if the stale or spoofing device is still on the LAN | Accepted for now (the till did not switch to it). Follow-up **ut-docs#2862** |
| m2 | minor | Other recoverable sync WARNs (pairing revoked, plugin re-fetch retries) still linger up to 24h | Follow-up **ut-docs#2862** |
| m3 | minor | de/ar/fa/tr `multitill.md` did not get the new sentence (structure unchanged, so guard-help-drift passes) | Accepted: translations follow via the usual help-translation pass |
| m4 | minor | Back-office panel filtering had no test | **Fixed**: `TestBackofficeModeRedirectsHome` asserts a resolved problem leaves the panel (red when reverted to `Recent()`) |
| n1 | nit | The issue-report bundle serialises `logging.Problem` with no JSON tags, so it gains PascalCase `Key`/`Resolved` | Accepted: consumers decode free text. Adding tags would rename the existing three keys |
| n2 | nit | Problems-ring tests assume the log level is WARN or lower | Pre-existing pattern |

The reviewer confirmed the following are fine:
- Ring locking (copy under the mutex, in-place resolve under the same mutex).
- docs-shots determinism (`clock.Now` for stamping and aging; the panel uses `maxAge 0`).
- ut-cloud replaces the device's problems on each report, so filtering on the till side does clear "Attention needed".
- Unkeyed pull errors are untouched by resolution.
- `ContactOK` re-arms the once-per-outage WARN.

## Verification

- TDD red/green was checked for every new assertion by reverting the fix, seeing the specific failure, and restoring it:
  - the prune tests (5 failures with WARN)
  - the enrolment self-repair test
  - the outage-recovery heartbeat assertion
  - keyed problems not aging out
  - the back-office panel test
- The CI-equivalent gate passed: `go test` over all packages, and pages/plugins with `-timeout 20m`, as `ci.yml` runs them (CI does not use `-race`).
- `golangci-lint` reported 0 issues. `gofmt` is clean.
- These guards pass: data-access, i18n, help-topics, help-drift, docs-shots, compliance-claims, competitor-naming, kiosk-engine, page-http-error.
- Local-environment exceptions:
  - The deadcode guard flags `logging.Stderr` and `timestampWriter` only because `cmd/unitill-desktop` is skipped here (no GTK headers). They are pre-existing.
  - The shellcheck guard failed because there is no shellcheck binary here. No shell files were touched.
- The Fable reviewer ran `-race` on logging, data, enroll and discovery: clean. `-race` on pages timed out in an unrelated wazero test under load. There were no DATA RACE reports.
- There is no visual surface change beyond hiding resolved lines in an existing list, so no screenshots.

## Deferred

- ut-docs#2857: a "Recent events" list (recoveries and self-repairs) from the till through ut-cloud to my. and admin.
- ut-docs#2862: key the remaining recoverable sync WARNs, and give the refused-proof WARN its own key.

Verdict: **safe to merge**.
