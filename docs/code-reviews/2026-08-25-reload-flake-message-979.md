# Code review: clarify TestReload_SurvivesRealisticPublisherContention's flake-vs-regression signal (ut-docs#979)

## Change

`internal/plugins/reload_busy_production_test.go` only — a doc comment and
a `t.Fatalf` message, no production code, no new tests. Recovered from an
orphaned branch (`fix/979-reload-flake-message`, committed by a prior
pipeline cycle but never turned into a PR — found by this cycle's daily
orphan-branch sweep) and merged now under this review record rather than
recreated from scratch, since the diff is unchanged from what a prior
cycle's investigation (recorded on ut-docs#979) already validated.

## Background (from ut-docs#979's own investigation, quoted for the record)

The test failed 3 times in ~24h in ordinary CI runs on commits that
touched no Go code near the plugin sync path, always
`SQLITE_BUSY`/"database is locked" under this test's deliberately
unthrottled write load. Reproduction attempt (`-race -count=5`, clean
local session, 4 cores) did not reproduce. This repo's `ci`/`e2e`/`build`
workflows run on GitHub-hosted `ubuntu-latest` (grepped — no
`self-hosted` label), so the flake is shared-runner variance, not
homelab queue contention. Conclusion: no evidence of a
`Manager.Reload`/`SyncPluginPaymentMethods` regression; a slower/shared
Actions VM can plausibly exceed `busy_timeout(5000)` on rare occasions
under this test's intentionally pessimistic load.

## Why this is safe

- The test's failure mode (hard `t.Fatalf` on any single failure) is
  **unchanged** — a genuine repeat regression still fails the build. This
  only corrects the message so the next reader checks for a *repeat*
  before concluding regression, instead of trusting the old message's
  "not a flake to retry away" framing on a single occurrence — which is
  what made three ordinary-variance flakes read as alarming.
- No behavior change to `Manager.Reload` / `SyncPluginPaymentMethods` —
  this only reduces the current test file's misleading annotation.

## Verification performed (this cycle)

- `gofmt -l internal/plugins/reload_busy_production_test.go` — clean.
- `go build ./internal/plugins/...` — clean.
- `go vet ./internal/plugins/...` — clean.
- `go test ./internal/plugins/... -run TestReload_SurvivesRealisticPublisherContention -count=1` — pass.
- Diff re-read line-by-line: comment-only + message-only change, no
  logic touched, matches ut-docs#979's own stated acceptance criterion
  ("message/comment-only change... no new tests needed").

## Note for the pipeline itself

The branch this PR ships was invisible to this cycle's zero-artifact
stale-claim sweep (ut-docs#812/#188 step 0c): that sweep searches for a
branch matching `-979-`/`-979$` (delimited), but the actual branch name
is `fix/979-reload-flake-message` — the issue number is preceded by `/`,
not `-`, so the pattern never matched. The sweep concluded "no branch"
~25 minutes before this review and reset ut-docs#979 back to
`status:ready`, unassigned — a false negative, not a true abandoned
claim. Filed as ut-docs#1031 to fix the matcher (also match
`<prefix>/<n>-` branch-naming, this repo's actual convention per
`git branch -a`, not just the `-n-`/`-n$` forms).

## Outcome

Merging as-is. Closes universaltill/ut-docs#979.
