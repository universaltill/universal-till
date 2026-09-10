# Code review — internal/pages CI-timeout margin (ut-docs#1992)

- **Date:** 2026-09-10
- **Branch:** `fix/1992-internal-pages-ci-timeout-margin`
- **Reviewer:** independent reviewer (fresh-context Opus subagent, isolated
  worktree — this pipeline's `complexity:medium` review tier).
- **Verdict: SAFE TO MERGE.** No blocking findings; two non-blocking comment-
  accuracy corrections, applied.

## What shipped

`universal-till` PR #1018's `ci`/`build` job's `Test` step timed out after
600s inside `internal/pages`, specifically inside
`TestResolveAndInstallBasePlugin_PicksHighestSemverVersion`. Filed as
ut-docs#1992 ("CI flake ... hung 600s"), reading the goroutine dump as a
possible hang mid-write in `InstallStatusRepo.Upsert`.

Investigation this cycle (re-verified independently by the review subagent
against the real CI log, run `34444881590` / job `102767420725`) found it is
**not a hang**: the flagged goroutine's stack is
`_walUnlockShared → …CommitPhaseTwo → sqlite3_step → InstallStatusRepo.Upsert`
— an ordinary in-flight `fcntl` unlock syscall mid-commit, not a blocked
wait. `Upsert` (`internal/data/install_status_repo.go:49`) is a single bare
`ExecContext`, no transaction held across calls, nothing to deadlock on.
`internal/db.Open`'s DSN already sets `busy_timeout(5000)` (rules out a
SQLite-lock explanation for a 600s block) and the connection pool is
unbounded (rules out `database/sql` pool exhaustion). The specific named
test passed 40/40 local reruns with `-race` across two independent runs
(~1.9s/iteration, 0 races).

This is the exact same symptom shape already investigated and documented in
this repo for a *different* package: `internal/plugins` hit the identical
"600s default per-package `go test` timeout dumps a goroutine mid-flight,
reads as a hang, isn't one" pattern (ut-docs#643/#753/#776, ut-docs#648) and
was fixed by pulling it out of the blanket `go test ./...` step into its own
step with `-timeout 20m`. `internal/pages`'s own plain local runtime
(~210-237s) leaves only ~2.5x margin against the 600s default, and — the
larger effect — inside the blanket step it also competes with ~70 sibling
test binaries; run in isolation (as the two existing OS-locale-specific
`internal/pages/...` steps already do) it lands at ~204-214s, well clear.

The fix mirrors the `internal/plugins` precedent exactly:

- `internal/pages` (exact package) excluded from the main `Test` step's
  blanket `go test $(go list ./... | grep -vE ...)` run.
- New dedicated step, `go test -timeout 20m ./internal/pages`, same
  placement pattern as the existing `internal/plugins` wider-timeout step.
- `-timeout 20m` added to the two existing OS-locale-specific
  `go test ./internal/pages/...` steps (en_GB, de_DE), which carry the same
  fixture cost under their own locale-specific contention.

No Go code changed — CI workflow only.

## Independent review — what was checked, and what it found

Dispatched to a fresh-context Opus subagent, isolated worktree, per the
`complexity:medium` review tier. It was told to actually run things, not
just read the diff:

1. **YAML validity and job structure** — parses clean; `build` job goes
   66 → 67 steps (exactly one net-new), correctly nested.
2. **Grep exclusion is exact, not a prefix accident** — ran `go list ./...`
   itself (73 packages) and confirmed the widened exclude pattern drops
   exactly one line, the bare `.../internal/pages`; the three subpackages
   (`catalog`, `common`, `itemsnav`, all effectively test-free) stay in the
   main step, and no unrelated package path collides with the pattern.
3. **No coverage gap, no accidental duplicate run** — modelled every
   `go test` step's package expansion before/after: `internal/pages` runs
   exactly 3x both before (main step, en_GB, de_DE) and after (new
   dedicated step, en_GB, de_DE) this diff. Coverage is relocated, not
   lost or duplicated.
4. **Margin, not a masked bug** — independently re-pulled the real failing
   CI log and confirmed the goroutine dump's own stack shows an in-flight
   syscall, not a block (see above); confirmed locally that
   `go test ./internal/pages` passes cleanly in ~235s. The wider timeout
   genuinely fixes a margin problem here rather than hiding a real one.
5. **20m is defensible** — matches the `internal/plugins` precedent's own
   chosen value; from the CI job's own successful retry, the isolated
   locale-specific `internal/pages/...` runs measured 204-214s, giving
   ~5.6x headroom against 20m. No `timeout-minutes` cap on the job.
6. **Comment-claim spot-check** — two inaccuracies found and fixed in this
   branch:
   - **[Fixed]** The step comment quoted `"FAIL ... internal/pages
     600.130s"`; the real CI log reads **600.022s**. Corrected.
   - **[Fixed]** The step comment's sibling-package slowdown figures
     (internal/pos 7.9x, internal/settings 7.4x, internal/pages/common
     7.2x) were directionally right but slightly understated versus the
     reviewer's own re-measurement from the actual CI log (8.8x / 8.1x /
     7.7x respectively). Corrected, and the comment now also states the
     larger effect plainly: isolation from sibling contention (~210s alone
     vs. >600s inside the blanket step) does more work here than the
     timeout bump itself, which is belt-and-braces on top of it.
   - All other spot-checked claims (`busy_timeout(5000)`, unbounded pool,
     the 40/40 `-race` rerun result, fixture-cost-not-one-slow-test as the
     real cost driver) verified accurate as written.
7. **Tooling scope** — confirmed nothing in `.golangci.yml` scans workflow
   YAML; the only repo guard that reads `.github/workflows/ci.yml` at all
   (`guard-webkit-version.sh`) still passes against the post-diff file.
   `go build ./...` clean (no Go source touched). No `CLAUDE.md` update
   needed — this adds a test step, not a new CI-blocking guard, so the
   "Before committing" guard list is unaffected.
8. **[Non-blocking, deferred]** `make test` (`Makefile`) still runs bare
   `go test ./...` at the 600s default, so a slow local machine can still
   hit the same wall outside CI. Out of scope for this CI-only fix — noted
   here rather than silently dropped; worth a Backlog card if it ever
   actually bites a local run (`test-race-pages`'s own `-timeout 60m`
   precedent is the pattern to follow if so).

## Verified beyond automated tests

- `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml'))"`
  — valid both before and after the comment-accuracy fixes.
- `go list ./...` re-run after the final edit to re-confirm the exclusion
  set is unchanged by the comment-only follow-up fixes.
- `go build ./...`, `gofmt -l .` — clean (no Go source touched by this
  change).
- Reviewer independently reproduced the `-race` rerun claim (40/40 PASS,
  two separate runs) rather than trusting the investigating session's word.

## Explicitly deferred

- `make test`'s own 600s default margin against a slow local machine —
  noted above, not actioned in this PR (CI-only scope).
- The underlying `internal/pages` fixture cost (~2200 tests each opening a
  fresh on-disk SQLite file and replaying all migrations) is the real
  driver of the package's runtime; a template/cached migrated-DB fixture
  would cut it further but is a separate, larger change, not this margin
  fix.
