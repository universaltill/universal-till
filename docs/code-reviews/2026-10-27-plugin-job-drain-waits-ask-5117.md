# Review: plugin-job drain waits for the plugin ask (ut-docs#5117)

**Scope:** `internal/pages/plugin_job.go`, `internal/pages/plugin_job_test.go`,
`web/help/img/manifest.json` (surface hash only).

## What shipped
- `TestPluginIdentify_UploadRefusals_3873` flaked once in a full
  `go test ./...` with `TempDir RemoveAll cleanup: … directory not empty`.
  Cause: `startPluginJob`'s inner goroutine (the one calling
  `AskPlugin`) was not counted in the test-only `pluginJobRegistry.wg`. A
  job that ends on its deadline or a cancel returns before its ask does,
  and the ask still runs `CheckPermission` and writes `auditDispatch` rows.
  So `drainPluginJobs` returned, and the harness cleanup closed the test DB
  and removed its folder while the ask was still writing to it.
- The inner ask goroutine is now counted too: `reg.wg.Add(1)` before
  `go func`, and `defer reg.wg.Done()` right after the required
  `defer logging.RecoverAndLog`. No production code waits on `wg`.
- Regression test `TestPluginJob_DrainWaitsForTheAsk_5117`.
- `web/help/img/manifest.json`: surface hash refreshed with
  `update-docs-shots-surface-hash.sh`. No rendered pixel changed.

## Review
Independent review by Sonnet in a fresh context (the author is Opus).
- **No blockers and no majors.** Checked: `Add` happens while the outer
  goroutine is still counted, so it cannot race `Wait`. On a panic the
  `done <- res` send (buffered) runs first, then `Done`, then the recover.
  `wg` is used only in tests. Every existing blocking handler is released
  before the harness drain.
- **Minor, accepted:** a few tests release a blocked handler with a plain
  `close(release)` in the test body. A `t.Fatal` before that line now adds
  a 5 s drain message to a run that has already failed. It changes nothing
  for a passing run.
- **TDD re-verified** by the orchestrator, and again by the reviewer (5/5).
  Without the fix the new test fails with "drainPluginJobs returned while
  the timed-out job's plugin ask was still running"; with it, the test
  passes 20/20 under `-race`.

## Verified
- Card acceptance: `go test -race -count=50 -run
  TestPluginIdentify_UploadRefusals_3873 ./internal/pages/` passes.
- `go test -race -count=10 -run 'TestPluginJob_|TestPluginIdentify_'
  ./internal/pages/` passes.
- `go build ./...`, `go vet`, `gofmt -l` (clean), `go test ./...`,
  `golangci-lint`.
- Guards in `ci.yml`: no failures caused by this change. Three could not
  run in this container: shellcheck is not installed, deadcode was built
  with go1.26, and `guard-docs-shots_test.sh` fails its dotfile case here.
  CI is authoritative for those three.

**Safe to merge.**
