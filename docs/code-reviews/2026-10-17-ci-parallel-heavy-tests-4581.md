# Review: `ci`'s heavy test runs move to a parallel `test-heavy` job (ut-docs#4581)

**Shipped:** `.github/workflows/ci.yml`: the five heaviest steps at the end of
`build` now run as legs of a new `test-heavy` matrix job, one runner per leg,
with no `needs:` and `fail-fast: false`. Each leg keeps its step's exact
command: the `internal/plugins` `-timeout 20m -v` run, `internal/pages`, the
en_GB and de_DE `internal/pages/...` reruns (their `LANG`/`LC_ALL` are now
set inline), and the boot-path `-race` set. Their comments move with them.
`build` keeps every guard, lint, build, the plain `Test` step and the
oauth/marketplace `-v` step. In run 39028767248 the moved steps were ~26 of
`build`'s 35 min, and `desktop-shell`/`windows-shell`/`e2e`/`contract` all
wait on `build`.

Also new: the wiring test `scripts/ci/ci-heavy-tests_test.sh`, run in
`build`. It checks that `test-heavy` exists with a timeout, `fail-fast: false`
and no `needs:`. It checks that each heavy command is a leg exactly once and
that `build` runs no whole-package run of a heavy package. It checks that
every package the plain `Test` step excludes is still run by some other
non-comment `go test`. A self-test on a synthetic workflow is included. No
test, guard or timeout was dropped or lowered. The universal-till `main`
ruleset has no required status checks, so the new job changes no merge gate.

**Reviewer:** an independent Fable subagent (the author was Opus 5.5). It
worked read-only, ran the gate itself and ran 11 mutations against scratch
copies.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The "excluded package still runs elsewhere" check counted comment lines as coverage. With `/internal/money$` added to the exclude plus a `# reproduce: go test ./internal/money` comment, the test passed. | Fixed: comment lines are dropped first. Author re-ran the mutation and it now FAILs. |
| 2 | minor | "build does not run" was an exact-substring match, so `go test -count=1 -v ./internal/plugins` re-added to `build` passed. | Fixed: any non-comment, non-`-run` whole-package `go test` of a heavy package in `build` fails. The self-test covers it, and so does an author-run mutation. |
| 3 | minor (cost) | All legs share setup-go's go.sum-keyed cache, so only one leg saves. The others compile cold, a few minutes each. | Accepted and recorded in the job comment. Wall time still lands well under the old serial `build`. |
| 4 | minor | `timeout-minutes: 30` = setup + cold compile + `-timeout 20m`, which can kill the runner before go test's timeout goroutine dump prints (the dump the #643/#1992 diagnoses relied on). | Fixed: raised to 40, with the reason in the comment. |
| 5 | nit | `fail-fast: false` wasn't pinned. | Fixed: pinned, and covered by the self-test. |
| 6 | nit | The job display name had nested parentheses. | Fixed. |

Checked and fine (reviewer):
- Each leg's command is byte-identical to the old `run:`. The inline env prefix reaches `go test` and its test binaries the same way the step `env:` did, under `bash -e`.
- No heavy test depends on an earlier `build` step: guests are built at test time, and nothing reads a prebuilt binary, `.syso`, node or `RUNNER_TEMP`.
- Nothing else in `scripts/`, `.github/` or the docs refers to the moved steps.
- actionlint v1.7.7 reports 0 findings. shellcheck, `guard-pipefail-grep-q.sh` and `guard-ci-tests-sandboxed.sh` are clean.

## Verified beyond automated tests

- TDD: the wiring test was written first. Against the unchanged `ci.yml` it
  failed with `FAIL - no test-heavy job`. On the branch, all checks pass.
- Author-run mutations, each restored afterwards: heavy package re-added to
  `build` with other flags → FAIL; new excluded package covered only by a
  comment → FAIL.
- This PR's own CI run is the end-to-end check of the split (leg timings,
  every leg green). The close-out comment on ut-docs#4581 records the result.

**Verdict:** safe to merge once CI is green. Deferred: none. (Per-leg build
caches stay an option if cold compiles turn out to dominate; the close-out
records the measured times.)
