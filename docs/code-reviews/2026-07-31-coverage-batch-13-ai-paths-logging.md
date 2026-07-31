# Code review — coverage batch 13: internal/ai, internal/paths, internal/logging

- **Date:** 2026-07-31
- **Branch:** `test/coverage-batch-13-ai-paths-logging`
- **Scope:** test-only (+~560 lines across 5 test files; no production code changed)
- **Card:** ut-docs#9 (coverage push remainder), batch 13
- **Independent reviewer:** Opus subagent (different model from the pipeline's), ran
  build/vet/tests itself including `-race`, `-shuffle=on -count=2` ×3, and its own
  14-mutation pass on top of the pipeline's 5.

## What shipped

| Package | Before | After (local, darwin) |
|---|---|---|
| `internal/logging` | 73.5% | **100.0%** |
| `internal/paths` | 69.7% | **91.0%** (linux CI also exercises the XDG branches of `Default`) |
| `internal/ai` | 68.3% | **95.7%** |

Highlights: the previously-0% `claude.go identify` now has real request/response
coverage against a local `httptest` fake of the Messages API (`option.WithBaseURL`,
zero external calls) — including the prompt-cache breakpoint placement (last ref
image vs system block), refusal stop-reason, empty/unparseable answers, and API
errors. `logging.Fatalf`'s never-returns contract is proven with a subprocess
re-exec (exit code 1 + message logged). `paths`' migration guard branches and
half-copy cleanups are exercised with real filesystem failure modes (blocked
target dirs, unreadable sources, EISDIR mid-copy).

## Mutation evidence (tests fail when the code is broken)

Pipeline pass (5/5 caught): Fatalf `os.Exit`, logf level filter, plugin half-copy
cleanup, Claude cache-breakpoint placement, refusal check.
Reviewer pass (12/14 caught; the 2 survivors became findings 1–3 below, both now
resolved — re-verified post-fix: the `MkdirAll(targetItems)` deletion and the
`copyFileBestEffort` cleanup deletion each now fail their test).

## Review findings and outcomes

1. **(should-fix, fixed)** "Still-legacy config → no-op" comments in
   `TestMigrateLegacyDBGuards`/`TestMigrateLegacyPluginsGuards` claimed to verify
   guards that are behaviourally subsumed by later guards (no input can
   distinguish them). Comments reworded to claim only what is observable.
   The underlying (harmless) guard redundancy in `paths.go` was deliberately NOT
   touched in a test-only batch.
2. **(should-fix, fixed)** The stray-top-level-file case couldn't detect deletion
   of the file branch's own `os.MkdirAll` (a sibling dir entry sorted first and
   created the target dir as a side effect) — the repo's recurring missing-MkdirAll
   bug class. Fixture renamed (`z-itm1`) so the file processes first; mutation
   re-run: now caught.
3. **(should-fix, fixed)** `TestCopyFileBestEffort` claimed "never half-copies"
   without testing it. Added an EISDIR mid-copy case asserting the truncated
   destination is removed (unix-only; skipped on windows). Mutation re-run: caught.
4. **(should-fix, fixed)** The blocked-target `migrateLegacyDB` case had no
   assertions; now asserts nothing was created and the blocking file is intact.
5. **(nit, fixed)** The three `chmod 0o000` cases would fail on Windows
   (`Geteuid()==-1`, chmod doesn't block reads): explicit `runtime.GOOS` skips
   added alongside the existing root skips.
6. **(nit, fixed)** Unchecked type assertions on the captured Claude request body
   would panic rather than fail with a message: replaced with a comma-ok helper.
7. **(nits, accepted as-is)** Subprocess test has no child-side timeout (parent
   timeout covers it); mid-test `t.Skip` under root reports the whole test as
   SKIP (cosmetic; CI never runs as root); three unrelated untracked planning
   docs in the tree (ut-docs#29 owns them) — excluded from this commit by
   explicit staging.

## Honestly deferred (not coverage theater)

- `identifyPrompt`/ollama-body `json.Marshal` error branches — unreachable with
  these types.
- `chat`/identify `io.ReadAll` mid-body failures — need fault injection.
- `migrateLegacyDB` `O_EXCL`-race and forced `io.Copy` failure branches.
- `paths.Default` windows/linux branches don't run on darwin — the test switches
  on `runtime.GOOS` honestly; linux CI covers its branch, windows has no CI test
  runner today.

## Verified beyond automated tests

- Full `go test ./...`, `go vet`, both CI guards (`guard-data-access.sh`,
  `guard-i18n.sh`) green before and after review fixes.
- `-race` and `-shuffle=on` (×3) on the three packages: no order dependence or
  global-state leaks (the `Init()`/chdir cleanups restore correctly).
- No secrets, no real client/shop names (fixtures: `com.example.*`, "Milk").

**Verdict: safe to merge.** Reviewer's must-fix list fully applied and
mutation-re-verified; remainder documented above.
