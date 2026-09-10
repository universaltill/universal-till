# Review: `newHermeticEnOnlyI18n` test-leak restore (ut-docs#2022)

**Card:** universaltill/ut-docs#2022 — `newHermeticEnOnlyI18n` installs a
hermetic, overlay-only, en-only translator via the process-global
`httpx.InitI18n` but never restored the real `web/locales` bundle afterward,
so it stayed wired for every test the Go test runner happened to schedule
later in the same binary — invisible at the leaking test's own call site and
dependent on file/test ordering. Found during the independent review of the
sibling card ut-docs#2015, which hit the identical hazard for real in a
*different* test helper (`newI18nForEntryOverlayTest`) and was fixed there
with a `restoreRealI18n(t)` `t.Cleanup` helper; #2015's own review left
`newHermeticEnOnlyI18n` explicitly out of scope and flagged it as this
follow-up.

**Build:** Sonnet (complexity:easy). **Review:** fresh-context Sonnet
subagent, isolated worktree, no access to Dev's own reasoning.

## What shipped

- `internal/pages/setup_base_plugins_test.go`: `newHermeticEnOnlyI18n` now
  registers a `t.Cleanup` that re-wires the real `web/locales` bundle
  (`config.NewI18n(filepath.Join("web","locales"), "en")` →
  `httpx.InitI18n`) once its calling test finishes. Inlined directly into
  this helper rather than calling #2015's shared `restoreRealI18n` — that
  helper isn't merged/available on this branch (#2015 is a sibling PR, still
  in review elsewhere). Best-effort by design, matching #2015's own
  helper: on error, no-op rather than fail the test's cleanup.
- New regression test,
  `internal/pages/i18n_hermetic_helper_test.go`'s
  `TestNewHermeticEnOnlyI18n_RestoresRealTranslatorOnCleanup`: exercises the
  leak directly (a subtest calls `newHermeticEnOnlyI18n`, so its own
  `t.Cleanup` fires when that subtest ends — simulating "the leaking test
  has already run" — then the outer test asserts the real bundle's locales
  (`ar`/`en`/`fa`/`tr`) are back), rather than depending on file-ordering
  luck the way the original #2015 incident was actually discovered.
  Confirmed failing pre-fix, passing post-fix (see TDD section).

## Independent review findings

Full independent pass (fresh-context Sonnet subagent, isolated worktree,
fetched the pushed branch directly — a first review attempt was voided
because its worktree predated the push and saw no diff at all; re-run
against the pushed `fix/2022-hermetic-i18n-leak-restore` branch). Verdict:
**SAFE TO MERGE**, no blockers.

1. **Fix is correctly timed** — `t.Cleanup` registered on the calling
   test's own `*testing.T` right after `httpx.InitI18n`/`dp.Pm.SetLocalizer`,
   so it fires on that test's own cleanup; no `t.Parallel()` anywhere in
   `internal/pages`, so ordering is deterministic. `config.NewI18n`'s
   relative `web/locales` path resolves correctly because `main_test.go`'s
   `TestMain` chdirs to the repo root once for the whole test binary, and
   the two tests that `os.Chdir` elsewhere (`receipt_test.go`,
   `static_page_test.go`) both restore cwd via their own cleanup first.
2. **Regression test is real, not tautological** — independently
   re-verified via revert-then-restore: reverting just the `t.Cleanup` block
   made the new test fail with the exact claimed message; restoring the fix
   made it pass again.
3. **No other caller broken** — `newHermeticEnOnlyI18n` is unexported;
   all 6 pre-existing call sites (plus the new test) build a fresh
   `common.Deps`/`Pm` per test and call it directly in their own test body,
   nothing relies on the leak persisting.
4. **No data race under `-race`** — `httpx.InitI18n`/`AvailableLocales` go
   through `atomic.Value`, and nothing in this package runs tests in
   parallel; `-race` run of the affected tests passed clean.
5. Two non-blocking nits, both deferred rather than fixed in this branch:
   - The restore's `config.NewI18n` error path silently no-ops with no
     `t.Logf` — currently unreachable given `TestMain`'s guaranteed chdir,
     but would leave no breadcrumb if it ever did trigger.
   - The restore logic is inlined rather than calling #2015's shared
     `restoreRealI18n`, since that helper isn't on this branch. Correct
     given the constraint; worth a trivial dedup once #2015 lands (both
     fixes will then carry byte-similar cleanup blocks).

## TDD re-verification (actual commands, actual output)

Reverted `newHermeticEnOnlyI18n`'s new `t.Cleanup` block (keeping the new
test):

```
=== RUN   TestNewHermeticEnOnlyI18n_RestoresRealTranslatorOnCleanup
    i18n_hermetic_helper_test.go:35: newHermeticEnOnlyI18n did not restore
    the real locale bundle once its calling test finished — the
    ut-docs#2022 leak is back: a later test in this binary would silently
    run against the hermetic en-only fixture instead of production locales
--- FAIL: TestNewHermeticEnOnlyI18n_RestoresRealTranslatorOnCleanup (0.13s)
```

Restored the fix, re-ran: `--- PASS (0.12s)`. Independently reproduced again
during the review pass on the pushed branch, same shape both times.

## Gate (run independently by Dev, then again by the reviewer, then again by
## the orchestrator after merging `main` in)

`gofmt -l .` (clean), `go build ./...`, `go vet ./...` (clean), full
`go test ./internal/pages/...` (all green, including `catalog`/`common`
subpackages), `golangci-lint run ./internal/pages/...` (0 issues),
`golangci-lint run ./...` (0 issues), `go test ./...` full repo suite (all
green), `scripts/ci/guard-data-access.sh` (clean — irrelevant to this
test-only diff, confirmed anyway). Test-only diff (`internal/pages/*_test.go`
only) — no `web/ui`, `web/locales`, or `web/help` files touched, so
`guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh` are not applicable to this
diff's content (all previously-passing on `main`, none of their surfaces
touched here).

## Verdict

**Safe to merge.** No blockers. Two nits noted above are legitimate
follow-up, not blocking: a `t.Logf` on the restore's silent error path, and
a dedup of the inline restore against #2015's `restoreRealI18n` once that
PR lands.
