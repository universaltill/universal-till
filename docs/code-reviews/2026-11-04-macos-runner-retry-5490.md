# 2026-11-04 — re-run runner-starved ios-ci / desktop-darwin runs (ut-docs#5490)

**Cause.** Since ~15:00 UTC on 2026-11-04 the macOS jobs (`ios-ci` →
`simulator` on `macos-26`, `desktop-darwin` → `build` on `macos-15`) often
sit queued with 0 steps and end `cancelled` after ~15 min. The check-run
annotations of runs 41874271565 and 41868844035 say "The job was not
acquired by Runner of type hosted even after multiple attempts" and "Due to
capacity constraints, jobs targeting macOS arm64 runners may experience
longer queue times." It is GitHub-hosted macOS arm64 capacity starvation:
the same workflow got a runner minutes later (run 41872905459, success). Not
our config and not billing (public repo, free minutes). Same class as
ut-docs#5175, which only covered the Release workflow.

**What shipped.** CI only, no product change.
- `.github/workflows/ci-runner-retry.yml`: `workflow_run` on
  `[ios-ci, desktop-darwin]` completed (non-success, non-skipped).
  `permissions: {}` at the top; the job has `contents: read`,
  `actions: write`, `checks: read`. It checks out the default branch only and
  passes every `${{ }}` value through `env:`, never into `run:`.
- `scripts/ci/ci-runner-retry.sh`: sources `release-runner-retry.sh` for
  `list_jobs` / `annotations_for` / `rerun_failed` / `listing_from` (one copy
  of the starvation detection). Its own pure `decide_ci` gives NONE /
  `REAL <names>` / `RERUN <names>`. "Re-run failed jobs" happens only when
  every failed job is a starved cancel, and only below `MAX_ATTEMPTS` (3).
  A fork PR's run, or an unknown head repository, is skipped with a notice.
- `scripts/ci/ci-runner-retry_test.sh` (34 cases, wired into `ci.yml` next
  to the release retry test); header notes in `ios-ci.yml` and
  `desktop-darwin.yml`; a paragraph in `docs/arch/desktop-app.md`.

**Pre-flight:** web/help n/a (no shop-owner-visible change); en.json keys
n/a; README n/a (no CI claims); ADR n/a (an extension of the #5175
mechanism); reference doc `docs/arch/desktop-app.md` updated; review record:
this file. Follow-ups: none.

**Phases.** BA/Architect folded into the card plus orchestrator notes
(easy card; cause established from run annotations). Dev: Sonnet subagent,
TDD (test failed first on the missing script). Tester + Review: independent
Opus subagent.

**Review findings** (all low; verdict merge):
1. A mutation dropping the `conclusion = cancelled` condition in
   `decide_ci` survived; through `main` it is harmless (annotations are
   only read for cancelled jobs), but the pure rule was unpinned. **Fixed:**
   cases (h5) timed_out+starved and (h6) failure+starved → `REAL`. The same
   mutation now fails (h5)/(h6).
2. Sourcing the script turns on `set -e` in the test. **Fixed:** `set +e`
   after the source, so a failing command counts as FAIL instead of aborting.
3. Note: `workflow_run` fires only once the file is on `main`. Post-merge
   check: `ios-ci` on a `main` push and a `workflow_dispatch` of
   `desktop-darwin` on `main`.

Security checked by the reviewer: no checkout or execution of the triggering
run's code, no `${{ }}` in `run:`, least-privilege permissions, fork runs
skipped (fails closed on an empty `HEAD_REPO`).

**Gates:** `ci-runner-retry_test.sh` 34/34; `release-runner-retry_test.sh`
66/66; `shellcheck scripts/ci/*.sh` 0 issues; `guard-ci-tests-sandboxed.sh`
ok; `guard-pipefail-grep-q.sh` ok. Honest limit: a re-run started with
`GITHUB_TOKEN` may not fire another `workflow_run`, so one automatic retry
in practice; a run starved twice still needs "Re-run failed jobs" by hand.
