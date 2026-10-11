# Review — parallel first compile of WASM plugins (ut-docs#3921)

- **Date:** 2026-10-11
- **Branch:** `feat/3921-parallel-wasm-compile`
- **Built by:** Claude Opus 5.5 (cloud session, lane:cloudsession)
- **Reviewed by:** Claude Fable 5.1 (independent subagent, isolated worktree)
- **Card size:** medium

## What shipped

- **`WasmRuntime.warmCompileCache`** (`internal/plugins/wasm_compile_warm.go`) runs in `Sync` before the serial load loop. When at least two distinct modules miss the compile cache, it compiles them concurrently. The serial loop then reads each one back from the cache.
  - It handles the first start after installs, updates or a wazero bump.
  - Plugins with identical bytes share one compile.
- **Exact attribution without the serial dir-listing diff.**
  - Each compile runs in its own wazero runtime over its own staging cache dir (`wasm-cache/staging/c-*`), so the single entry wazero writes there belongs to that module.
  - wazero names an entry `sha256(moduleID ‖ magic ‖ CPU features)`. Every input is fixed for the process, so the entry gets the same name `cachedRT` looks up.
  - The entry is renamed into the shared version dir and indexed under `w.mu`. The serial path holds that lock across its snapshot→compile→attribute diff, so neither path sees the other's writes.
  - Staging leftovers from a killed start are removed when the cache opens.
- **Bounded concurrency.**
  - At most GOMAXPROCS compiles run at a time (`w.compileWorkers` overrides this in tests).
  - On Linux, the count is lowered further so that 32× the size of the largest modules fits in half of `MemAvailable`. The cut is logged.
  - If only one worker is left, the parallel pass is skipped and the serial path runs as before.
- **Failure handling.** A compile, rename or panic failure in the parallel pass only leaves that plugin to the serial path. A broken module is still marked `broken`, and the cache stays on.
- **Android, iOS and a cache that is off** are unchanged. With no cache dir, the pass returns at once.
- **Logging and docs.**
  - A new load outcome, `compiled in parallel`.
  - Per-module `wasm compile <id>@<ver>: compiled in <dur>` lines and one summary line.
  - Docs: `docs/performance.md` here, and ut-docs `architecture/wasm-runtime.md` (separate PR).

## Measurements

linux/amd64 (cloud container), 4 cores, `ut-plugin-tax-uk` (5.1 MiB). Each mode ran in its own process with `TestWasmCompileWarm_Measure`, twice:

| modules | serial (old path) | parallel | peak RSS serial → parallel |
|---|---|---|---|
| 3 | 7.2 s / 6.9 s | 2.79 s / 2.73 s | 240 → 452 MB |
| 4 | 10.1 s / 9.9 s | 3.12 s / 3.06 s | 252 → 565 MB |

Each extra concurrent compile adds about 110 MB, roughly 21× the module size. The memory bound uses 32× to leave headroom for arm64.

**Not measured: a Pi 4**, which the card's acceptance criteria name. A cloud session can't reach that hardware. It is filed as a `blocked:env` follow-up card, with the harness recipe (`GOARCH=arm64 go test -c`, then the `UT_WASM_COMPILE_*` env vars).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `CompilesMissesConcurrently` passed under serial execution too. The first compile timed out, then the second saw the channel closed. | **Fixed.** The test now records the maximum number of compiles in flight and asserts at least 2, with a 5 s wait. Re-verified: with the semaphore forced to 1 it fails with "at most 1 compile in flight", and it passes when restored. |
| 2 | should-fix | A recovered panic in a worker left `entry == ""`, and the rename then logged a misleading error. | **Fixed.** An empty entry is now reported as "compile did not finish" and skipped before the rename. |
| 3 | should-fix | The `warmed` mark survived a `load` that failed to read the file, so a later genuine cache hit was labelled "compiled in parallel". | **Fixed.** `load` takes and clears the mark before the read. Sync's module-drop loop clears it too. |
| 4 | nit | The module bytes of would-hit candidates were held during the compiles, and the memory bound didn't count them. | **Fixed.** The candidate slice is dropped before the pass. Jobs keep only the bytes of the misses. |
| 5 | nit | It was silent when memory lowered the worker count. | **Fixed.** One info line gives the job count, the allowed concurrency and the available MiB. |
| 6 | nit | `MemAvailable` ignores cgroup limits inside a container. | **Accepted, documented in the code comment.** The target is a Pi till, not a container. |
| 7 | nit | There was no review record in the WIP snapshot. | **Fixed:** this file. |

The reviewer checked and found sound:

- **Concurrency:** no data race under `-race`. `Reload`/`Sync` is serialised by `Deps.PluginMu`. Rename, index and `warmed` all change under `w.mu`.
- **Cleanup:** `prune` and `wipe` never touch `staging/`.
- **Panic handling:** the worker's defers run in the right order on a panic (`<-sem`, then `wg.Done`, then `RecoverAndLog`).
- **File writes:** every write has `os.MkdirAll` first. All paths derive from `w.cacheDir`, `w.baseDir` or `/proc/meminfo`, never the cwd.

## Verified beyond the unit tests

- **TDD:**
  - With the `warmCompileCache` call disabled, the parallel tests fail: `CompilesMissesConcurrently`, `AttributesEachEntry`, `SharedBytesCompileOnce` and `BrokenModuleLeftToSerialPath`.
  - With the semaphore forced to 1, the concurrency test fails.
- **Attribution:** `AttributesEachEntry` deletes one plugin's recorded entry, and on restart exactly that plugin misses. Uninstalling a plugin prunes exactly its entry.
- **Race run:** `go test -race` over the cache and warm tests passed, both locally and in the reviewer's isolated run (617 s, no data race).
- **Full gate:**
  - gofmt, `go build ./...`, `go vet ./...` and `golangci-lint` (0 issues) all pass.
  - Every guard in the `build` job passes, except `guard-shellcheck-version`: the container has no `shellcheck` binary, and CI's runner does.
  - `go test ./...` passes, and `internal/plugins` passes with CI's `-timeout 20m`.
- **No UI surface**, so there is no help topic or screenshot.

## Verdict

Safe to merge. Deferred: the Pi 4 measurement (the follow-up card), and a cgroup-aware memory bound if containerised tills ever ship.
