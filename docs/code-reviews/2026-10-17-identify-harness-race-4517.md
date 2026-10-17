# Code review — plugin job harness data race (ut-docs#4517)

Date: 2026-10-17 · Branch: `fix/4517-identify-harness-race` · Author model: Sonnet (Dev) · Reviewer: Opus (different model, per MODEL-ROUTING)

## What shipped

`TestPluginIdentify_JobCarriesReadOnlyOperator_4444` failed under `-race`
on `main`. The 4444 tests return as soon as the plugin handler has run,
while the job goroutine started by `startPluginJob` is still finishing and
runs the identify seam's `keep` (`onDone`), which reads the package global
`pluginJobs` (`plugin_identify.go`). The harness cleanup then restored that
global, which is a write racing that read.

- `internal/pages/plugin_job.go`: `pluginJobRegistry` gains a
  `wg sync.WaitGroup` that counts `startPluginJob`'s goroutines.
  `wg.Add(1)` runs before `go func()`, and `defer wg.Done()` is the
  goroutine's first defer, so it runs last. Production behaviour is
  unchanged.
- `internal/pages/plugin_job_test.go`: `drainPluginJobs(t, reg)` cancels
  the registry's running jobs, then waits (at most 5s) on `wg`. On timeout
  it calls `t.Errorf`, because a leaked job goroutine is a test failure.
- Both harnesses that swap the job globals (`newIdentifyHarness` and
  `newJobHarness`) drain the registry they installed before restoring the
  globals.

## Findings

1. **(fixed, medium)** Dev's first version polled the registry until no
   job was `running`. That missed jobs that `cancelRunning` or `abandon`
   had already deleted from the map: the identify "new capture supersedes"
   path calls `cancelRunning` in production code, and such a job can still
   be running `onDone`. Reworked to the WaitGroup, which covers every job
   goroutine.
2. **(accepted)** A plugin handler that ignores its context keeps only the
   inner `AskPlugin` goroutine alive. It is outside `wg`, but it touches
   only the harness's own mutex-guarded state, not the swapped globals.
3. Checked and fine:
   - `Add` happens before the `go` statement.
   - No `Add` runs concurrently with `Wait`: requests are synchronous in
     httptest, and cleanup runs after the test body.
   - `go vet` copylocks passes: the registry is only used by pointer.
   - No file writes or paths are involved, so the `os.MkdirAll` and
     `paths.Data` checks don't apply.
   - No UI, i18n, SQL or migrations are touched.

## Verification

- Before: `go test ./internal/pages/ -run TestPluginIdentify_JobCarriesReadOnlyOperator_4444 -race -count=3`
  on `main` reported `WARNING: DATA RACE`. The write was at
  `plugin_identify_test.go:51` (cleanup) and the read at
  `plugin_identify.go:276` (`keep`).
- After:
  - the two 4444 tests with `-race -count=20`: ok
  - the identify and job tests with `-race -count=3`: ok, no race
  - the full `internal/pages` package with `-race`: see the gate below
  - `gofmt`, `go build ./...`, `go test ./...`, `golangci-lint run ./...`
- No visual surface: this is test infrastructure only.

## Verdict

Safe to merge.

## CI follow-up

The first CI run failed `guard-docs-shots`, because a file under
`internal/pages/**.go` changed. The change alters no rendered pixel: it
adds a WaitGroup field and the calls that use it. So the surface hash was
refreshed with `scripts/ci/update-docs-shots-surface-hash.sh` and committed
with the `Docs-Shots-Unchanged: true` trailer, and no screenshots were
regenerated.
