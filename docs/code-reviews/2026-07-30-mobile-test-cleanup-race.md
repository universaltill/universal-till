# Code review — mobile test data-dir cleanup flake (main CI red)

- **Date:** 2026-07-30
- **Branch/PR:** `fix-mobile-test-cleanup-race`
- **Author:** pipeline (fable)
- **Independent reviewer:** opus subagent (different model)
- **Scope:** `mobile/mobile_test.go` only (tests-only)

## What happened

Post-merge `ci` on main (run 30531313594, the PR #95 merge commit) failed:
`TestIsRunning_DetectsServerDiedWithoutStop` →
`testing.go:1369: TempDir RemoveAll cleanup: unlinkat .../002: directory not
empty`. Neither PR #95 nor #96 touched `mobile/` — an intermittent flake
that had been latent.

## The review earned its keep — the first diagnosis was wrong

The author's first fix assumed a LIFO-ordering bug (`t.Cleanup(Stop)`
registered before the data-dir `t.TempDir()`, so RemoveAll would run before
Stop). The independent reviewer **disproved this** by reading Go's
`testing.makeTempDir` (the RemoveAll cleanup is registered only on a test's
FIRST `t.TempDir()` call — which is the env-file one inside `mobileTestEnv`,
already before `Stop`) and by empirically mirroring both call orders: Stop
ran before RemoveAll in BOTH — the reordering was a no-op, and its comment
would have misdirected future debugging.

**Real root cause (reviewer's, verified against source):**
`internal/app/app.go` starts `updates.Start`, `alerts.Start`, `enroll.Init`,
and the plugin supervisor as detached goroutines and never joins them.
`app.Run` returns as soon as the HTTP server stops, `mobile.Stop()`'s
`<-done` unblocks, and a straggler goroutine can still write into the data
dir (sqlite `-wal`/`-shm` etc.) while `RemoveAll` scans it — "directory not
empty".

## What shipped (final shape)

- `mobileTestEnv` now owns the data dir via `os.MkdirTemp` (outside
  `t.TempDir`'s cleanup machinery) and removes it in its own cleanup with a
  5s retry window; `Stop` is registered after, so it runs first (LIFO). This
  deterministically absorbs a straggler's last writes instead of racing them.
- The helper's comment states the true cause and points at the queued
  product fix.
- Readability keep from the first attempt: tests use the returned data dir
  instead of inline `t.TempDir()` calls.

## Deferred (queued in ut-docs/QUEUE.md as its own 🟡 item)

The product-shaped gap: `app.Run` should drain its background services
before returning so "stopped" means no surviving writer (matters for the
Android/iOS lifecycle path, not just tests). Plus the reviewer's nit:
`mobile.Start` sets `UT_DATA_DIR` via raw `os.Setenv`, never restored.

## Verification

- Reviewer: LIFO claim empirically tested both ways; edge cases traced
  (`TestStart_DifferentDataDirWhileRunningErrors`' inline dir never sees a
  server write — Start fails before boot; `FastFail`'s failed Start leaves
  no survivor); `go vet` + `go test ./mobile/ -count=5` green; verdict SAFE
  TO MERGE with the comment correction, which was applied.
- Author, post-correction: `go build ./...`, `go vet`, `go test ./mobile/
  -count=5` green. The flake is timing-dependent (never reproduced locally
  at `-count=15` pre-fix; one CI occurrence), so the proof here is the
  traced mechanism plus a mitigation that cannot race by construction —
  stated honestly rather than claiming a red/green repro.

## Verdict

**SAFE TO MERGE** (tests-only; misleading first-diagnosis comment corrected
before commit; durable fix queued, not silently dropped).
