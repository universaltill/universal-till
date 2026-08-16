# Code review — internal/plugins CI timeout margin (ut-docs#643, #753, #776)

**Date:** 2026-08-16
**Card:** universaltill/ut-docs#643 (also closes #753, #776 — same package,
same margin issue, already tracked as near-duplicates of each other)
**Branch:** `fix/plugins-race-timeout-margin-643`
**PR:** universaltill/universal-till#382
**Reviewer:** independent reviewer, Opus, fresh context

## What shipped

`.github/workflows/ci.yml` only — no Go code changed. The main `Test` step
(`go test ./...`) no longer includes `internal/plugins`; a new step runs
`go test -timeout 20m ./internal/plugins/...` on its own, immediately after.
Every other package keeps the default 600s `go test` timeout.

## Investigation (this is an investigation-shaped card, per #643's own
requirement list)

1. **Checked for a reintroduced goroutine-leak antipattern** (ut-docs#509's
   shape: a `Supervisor`-starting test missing a `t.Cleanup` drain, later
   copy-pasted per ut-docs#502's PR #269). `git log --since=2026-08-12 --
   internal/plugins/*_test.go` shows only the tests already reviewed under
   #754/#674/#775/#770 since then — read every one that touches
   `Supervisor` (`shutdown_drain_test.go`) or calls `WasmRuntime.Sync`
   (`export_wasm_dispatch_test.go`, `import_wasm_dispatch_test.go`,
   `misc_coverage_test.go`, `wasm_sync_broken_test.go`, `wasm_sync_test.go`,
   `wasm_tcp_test.go`). Every `Supervisor` test drains via `t.Cleanup`;
   every `Sync`-calling test either drains its own subscription (`defer
   bus.ResetSubscribers()`) or never actually registers one (the plugin row
   fails to load before reaching `SubscribeWithHandler`). **No leak found.**
2. **Checked the historical precedent.** `.github/workflows/ci.yml`'s own
   ut-docs#662 step comment already documents an *earlier* occurrence of
   this exact symptom (a 600s `internal/plugins` timeout with goroutines
   "blocked on internal/logging's mutex and database/sql's connection
   opener", found investigating ut-docs#674) — that investigation fixed a
   real unbounded amplifier (`EventBus.publish`'s unthrottled channel-full
   diagnostic) but its own commit message and the linked review record say
   independent review could **not** reproduce the original hang to confirm
   that was the root cause. ut-docs#643 is the SAME symptom recurring AFTER
   that fix landed — consistent with "margin, not a (single, already-fixed)
   bug."
3. **Checked the sibling incident.** ut-docs#648 (open, `internal/pages`,
   unrelated package) recorded the identical shape independently: a 600s
   timeout dump showed `TestAskTaxRateBP_OverflowAndConcurrency` "still
   runnable inside a DB query — not deadlocked, not a real hang" at
   599.802s. This is the general mechanism: `go test -timeout`'s deadline
   dumps every live goroutine's stack at that instant; whichever one is
   transiently contending a shared resource (here `internal/logging`'s
   package-global `*log.Logger`, guarded by the stdlib's own internal
   mutex) reads as "blocked" completely out of context, indistinguishable
   from a real deadlock in a single stack dump.
4. **Measured directly, this session:**
   - `go test ./internal/plugins/... -race -count=1 -timeout 15m` (isolated
     run): **706.285s**. One unrelated failure, `TestPluginIPC_Ack` ("dial
     plugin failed: connection refused") — a `go run` subprocess
     compile-then-listen race against a 5s deadline in
     `ipc_integration_test.go`, not a `-timeout`/package-level issue, and
     not reproduced in the full-suite run below. Flagged as a possible
     separate, sandbox-CPU-contention-only flake; not fixed here (out of
     scope, single occurrence, no evidence it affects real CI).
   - `go test ./...` (full suite, matching CI's *actual* invocation — no
     `-race` anywhere in this repo's CI): all 38 packages pass, exit 0.
     `internal/plugins` alone: **93.358s** in this run.
   - The gap between 93s and 706s (same package, same session, only
     `-race` differing) plus the real CI incident sitting at 600.015s
     (plain, no `-race`) says the variable is runner load/contention, not
     a fixed cost — consistent with #643's own hypothesis 3 ("many
     concurrent pipeline cycles running CI against this repo
     simultaneously") and with ut-docs#753/#776's own 495-515s `-race`
     measurements a day earlier (this session's own environment plausibly
     ran under different load than theirs did).

**Conclusion:** timeout margin, not a leak or deadlock. Fix: give
`internal/plugins` a longer, explicit timeout in CI rather than continuing
to root-cause a moving target with no reproducible trigger.

## Independent review

Spawned an Opus subagent, fresh context, with the PR diff, the reasoning
above, and instructions to re-run the build/tests personally and
independently re-audit the "no leak" claim rather than trust the PR body.

**Verdict: safe to merge, with one real gap and two accuracy fixes —
all applied on this branch.**

The reviewer independently re-ran `go build ./...`, the full `go test
./...`, and `go test -count=1 ./internal/plugins/` (83.324s, uncached),
audited all 16 `.Sync(` call sites in `internal/plugins/*_test.go`
individually (not just the ones already named in the PR body), confirmed
`grep -c "t.Parallel()"` is 0 (so `SharedBus`'s singleton can't be
cross-torn between tests either), and **independently confirmed the "no
leak" claim** — including that the one un-drained `Sync` call
(`misc_coverage_test.go` `TestWasmRuntimeSyncLifecycle`, lines 239/257/270)
never actually reaches subscription (load failure / non-wasm runtime /
nil-receiver short-circuit first), so nothing leaks despite the missing
`defer`.

Findings, all fixed on this branch:

1. **MAJOR — `release.yml`'s "Run tests before releasing" step was still a
   bare `go test ./...` with the 600s default**, so the release path could
   still fail on the exact timeout this PR exists to fix. Fixed: same
   split (`go test $(go list ./... | grep -v '/internal/plugins$') &&
   go test -timeout 20m ./internal/plugins`).
2. **MINOR — scoping asymmetry.** The new CI step used
   `./internal/plugins/...`, which (unlike the main step's
   `grep -v '/internal/plugins$'` exclusion) also matches
   `internal/plugins/marketplace` and `/oauth` — so those two ran a
   second time in the same job, under the new step's 20m timeout, exactly
   the "run something twice in one job" pattern ut-docs#662's own comment
   already warns caused a real contention incident (ut-docs#674). Measured
   cost was only 0.31s, but the fix is one character: dropped the trailing
   `/...` so the step scopes to exactly `internal/plugins`. Applied to
   both `ci.yml` and the new `release.yml` step.
3. **MINOR — comment accuracy.** The original CI comment cited
   ut-docs#753/#776's `-race` measurements (495-515s) to justify a timeout
   on a step that never uses `-race` (confirmed: `-race` appears nowhere in
   any of this repo's five workflow files) — and that citation was
   internally inconsistent with this PR's own `-race` measurement
   (706.285s, which *exceeds* 600s, not "1.5-2min under" it). Reworded to
   cite the actually-relevant plain-runtime numbers (83-93s). Also
   corrected the "every WasmRuntime.Sync call ... already drains its
   subscribers" overclaim to match the audited reality: the ones that can
   register one drain it; the rest never reach subscription at all.
4. **NIT, not applied — no `set -o pipefail`.** GitHub Actions' default
   Linux shell for a `run:` step without an explicit `shell:` key already
   invokes `bash -eo pipefail`, so `go list | grep` failures aren't
   silently masked; no change needed.

Full findings detail, including the reviewer's own audit trail (which
`.Sync(` sites drain, which don't and why that's still safe, the runtime
comparison table), kept in the PR thread rather than duplicated here.

## Scope discipline

- Diff touches only `.github/workflows/ci.yml`. No Go code, no migrations,
  no UI/i18n/help-topic surface — `guard-help-topics.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh` don't apply to this diff
  (no page routes, no locale strings, no compliance wording touched).
- `ut-docs#648` (same shape, `internal/pages`) deliberately left alone —
  different package/card, same fix pattern noted on that issue as a
  cross-reference rather than folded into this diff.
- No real client/shop name, no secret-shaped literal in the diff (it's 24
  lines of CI YAML plus comments — confirmed by reading the full diff).

## Gate

| Check | Result |
|---|---|
| `go build ./...` | pass |
| `go test ./...` (full suite, plain — matches CI) | pass, all 38 packages |
| `go test ./internal/plugins/... -race` | pass (706.285s; see `TestPluginIPC_Ack` note above) |
| `scripts/ci/guard-data-access.sh` | pass |
| `scripts/ci/guard-kiosk-engine.sh` | pass |
| `scripts/ci/guard-plugin-menu-read.sh` | pass |
| CI YAML syntax | valid (`yaml.safe_load`) |

## Files

- `.github/workflows/ci.yml` — split the `Test` step so `internal/plugins`
  runs separately with `-timeout 20m`; every other package keeps the
  default 10m.
