# Review — persistent WASM compile cache (ut-docs#3912)

- **Date:** 2026-10-08
- **Branch:** `perf/3912-wasm-compile-cache`
- **Built by:** Claude Opus 5.5 (Dev subagent)
- **Reviewed by:** Claude Fable 5.1 (independent subagent, isolated worktree)
- **Card size:** medium

## What shipped

- **A persistent wazero compilation cache** at `paths.Data("wasm-cache")`, written by `NewWasmRuntimeWithCache` and wired in `plugins.Init`. It is off on Android and iOS, which use the interpreter.
- **A cache-free fallback runtime.** wazero turns any cache read or write failure into a `CompileModule` error. When that happens the module is compiled again without the cache, the cache is wiped, and it stays off until the process restarts. Each module is instantiated in the runtime that compiled it.
- **A size bound.** `index.json` records which cache entry belongs to each plugin. At the end of every Sync the till deletes:
  - entries that no installed plugin references (uninstalled plugins, old versions);
  - leftover `*.tmp` files;
  - directories left by other wazero versions.

  A disabled plugin keeps its entry.
- **A log line per load:** `wasm load <id>@<ver>: compiled|cache hit|compiled without cache in <dur>`.
- **A new data-layer query, `PluginRepo.ListWasmPluginIDs`,** which also returns disabled plugins. If the query fails, that Sync skips the prune.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Nothing tested the runtime used after a cache hit. With a mutant that instantiates a hit module in the plain runtime, every submitted test still passed. In production, every plugin would fail after the first restart (`source module must be compiled before instantiation`). | **Fixed.** `RestartHitsCache` now runs an event after the hit and checks the guest's storage write. The reviewer confirmed this test fails on the mutant. |
| 2 | minor | No test proved that a genuinely invalid module leaves a healthy cache alone. The code was correct. | **Fixed.** Added `TestWasmCompileCache_InvalidModuleKeepsCache`: the cache stays on, its entry stays, and the invalid plugin is marked broken. |
| 3 | minor | If the cache keeps failing (disk full or read-only), the first plugin is compiled twice on every start, about 21 s on a Pi 4. One in-memory module leaks per process. | **Accepted.** The failure is logged, and the cost is bounded to once per process. |
| 4 | minor | Disabling and then re-enabling a plugin recompiled it, about 10 s on a Pi 4. | **Fixed.** The prune now keys on installed plugins rather than active ones, using `ListWasmPluginIDs`. New tests: `DisabledPluginKeepsEntry` (failed first) and `UninstalledPluginEntryPruned`. |
| — | nit | The wazero version is worked out the same way wazero's internal `version` package does it. | **Accepted.** If they ever drift, the cache turns itself off. |
| — | nit | If a stale entry is rewritten within the filesystem's mtime granularity, the log line says "cache hit" when it should say "compiled". | **Accepted.** Cosmetic; the index stays correct. |

The reviewer checked and found sound:

- **Concurrency:** all cache access is under `w.mu`, and Sync is serialised by `PluginMu`.
- **Closing modules:** `CompiledModule.Close` is bound to the engine that compiled it.
- **Prune safety:** it only deletes names read from the two directories, skips symlinks, and never builds a path from a plugin id.
- **Security:** the directory is 0700 and the files 0600. Anyone who can write the cache can already write `data/plugins/*/plugin.wasm`, which is loaded at every start without re-verification. **No new attack vector.**
- **File writes:** the directory is created with `MkdirAll` before every write, and the paths come from `paths.Data`.

## Verified beyond automated tests

- **Pi 4 Model B** (192.168.1.167, Debian 13, aarch64), real `ut-plugin-tax-uk` `plugin.wasm` (5.1 MB), real `WasmRuntime.Sync` in three separate processes sharing one cache directory:

  | Start | Load time | Outcome |
  |---|---|---|
  | 1 | 10.97 s | compiled |
  | 2 | 0.48 s | cache hit |
  | 3 | 0.48 s | cache hit |

  The cache is one 23 MB entry. AC "well under 1 s": **met**.
- **TDD re-verified by the orchestrator** in a separate worktree:
  - a no-op `prune` fails the inactive-plugin, version-bump and stale-dir tests;
  - forcing the cache off fails `RestartHitsCache` and `CorruptEntryFallsBackAndWipes`.
- **Gate:**
  - `go build`, `go vet` and `go test ./...` all pass.
  - `-race` passes on the cache tests.
  - The data-access, i18n and core-neutral guards pass.

There is no UI surface and no user-facing strings, so the UX, i18n and help passes don't apply.

## Deferred

- ut-docs#3921 — compile plugins concurrently on the first start.

## Verdict

Safe to merge.
