# Review: CI runs `internal/app` and `internal/server` under `-race` (ut-docs#3014)

**Shipped:** `.github/workflows/ci.yml` — the existing `-race` step
(`internal/enroll`, `internal/logging`, `internal/netreach`) now also runs
`./internal/app/` and `./internal/server/`; the plain `Test` step's
`go list | grep -vE` exclude gains both so they don't run twice. Comments
updated (why these join, why pages/plugins/data stay out, expected step
time). `Makefile`'s `test-race-pages` comment no longer claims CI never runs
`-race`. No Go code changed.

**Reviewer:** independent Fable subagent (author Opus 5.5), in its own
worktree; re-ran the race step and the mutation check itself.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `Makefile:28-29` said "-race isn't run in CI" and pointed at the internal/plugins step comment this diff rewrites (stale since #3021) | Fixed — describes the boot-path `-race` step |
| 2 | nit | Step-time comment ignored the cold race-instrumented compile of `internal/app`'s deps (expect ~3-6 min; build job ~30 min, no job timeout at risk) | Fixed — comment states it |

Checked and fine: the new regex excludes exactly `internal/app` and
`internal/server` from the plain step (old vs new `go list` diff, 104
packages); every excluded package runs in exactly one later step; YAML
parses; cited issues (#2990, #1034, #2156, #1366, #3021, #3410) match;
no `scripts/ci/*` copy of the regex or package list; same cache env as the
sibling steps; no flaky-timing polarity in either package's tests (no
`t.Parallel`, the 150ms `waitWithin` checks assert *not done*).

## Verified beyond automated tests

- Step's exact command on the branch: all five packages `ok` (app ~149s,
  server ~46s locally; reviewer: 153s / 47s).
- Mutation (acceptance 2), run by author and reviewer independently:
  `cfg.Demo = cfg.Demo` inserted before `bindListener` in `server.Start` →
  plain `go test -run TestStart_DoesNotWriteSharedConfigWhileOthersReadIt`
  passes (the race hides), `-race` reports `DATA RACE` and FAILs; file
  restored, tree clean.

**Verdict:** safe to merge. Deferred: none (a nightly `-race` run for the
big packages stays a possible separate card, as the issue notes).
