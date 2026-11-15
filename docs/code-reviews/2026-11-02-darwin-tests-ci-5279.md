# Review: run darwin-only tests in CI (ut-docs#5279)

Branch `ci/5279-darwin-tests`, PR universal-till#2286. A Sonnet dev agent
built it. An independent Opus reviewer reviewed it, and the orchestrator
checked the CI evidence.

## What shipped

- `desktop-darwin.yml` (macos-15) has a new step, "Test packages with
  darwin-only files". It finds packages from tracked files only, so
  `.claude/worktrees/` and untracked files are never included. A package
  counts when it has a `*_darwin.go` or `*_darwin_test.go` file, or a
  `//go:build` line that names `darwin` without a `!` directly in front.
  The step runs `go test -count=1` on each one. Today that is `internal/db`,
  `internal/diskspace`, `internal/lantls` and `internal/selfupdate`.
  - `cmd/unitill-desktop` is excluded: the steps above build and vet it.
  - If discovery finds nothing, the step fails.
- New `paths:` triggers:
  - `internal/selfupdate/**`
  - `internal/lantls/**`
  - `internal/diskspace/**`
  - `internal/db/lock_unix.go`
  - `**/*_darwin.go`
  - `**/*_darwin_test.go`
- It stays path-scoped and is never a required check.
- `ci.yml`: the Linux cross-vet `GOOS=darwin go vet` now covers all four
  packages on every PR. Before, it covered `lantls` only. The stale comment
  "nothing else compiles darwin-tagged code" is replaced.
- The `CLAUDE.md` bullet is updated to match.

## Acceptance proof (AC: a broken darwin-only test makes the job red)

- **Red:** commit `235d3ac` deliberately inverted
  `TestSupportedFalseOnIntelMacAppBundle`. In desktop-darwin run
  41548556094, the step "Test packages with darwin-only files" failed on
  `--- FAIL: TestSupportedFalseOnIntelMacAppBundle
  (macapp_darwin_test.go:37)`. `db`, `diskspace` and `lantls` passed.
- **Green:** commit `d1a8c60` reverts the break. The run on the final head
  is linked in the PR's close-out comment.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| m1 | minor | `paths:` missed `internal/db/lock_unix.go` and `internal/diskspace/diskspace_bsd.go`. Both are compiled for darwin but not named `*_darwin.go`, so a darwin-only break there would merge green. | **Fixed**: both added to `paths:`. All of `internal/db/**` is deliberately left out, because it changes on most PRs and macOS runner minutes are costly. The ci.yml cross-vet also compiles all four packages for darwin on every PR. |
| m2 | minor | `cmd/unitill-desktop` is excluded from testing, so a future `*_darwin_test.go` there would never run. | **Accepted**: none exists today. The workflow comment says a new one needs its own step. |
| n1 | nit | The regex `[^!]darwin` also matches `!(darwin && …)`, so the comment overstated it. | **Fixed**: the comment now says the match is approximate. An extra package only costs test time. |
| n2 | nit | `*_darwin_arm64.go` names and legacy `// +build` lines are missed. | Accepted: none exist. |
| n3 | nit | Stale wording in the ci.yml comment. | **Fixed**, as above. |

## Verified

- I extracted the discovery block and ran it under bash. It found
  exactly the four packages.
- `shellcheck` is clean. The one `SC2086` suppression is targeted, with
  its reason on the line above.
- `GOOS=darwin go vet` of the four packages passes.
- Linux `go test` of the four packages passes.
- YAML parses.
- The workflow-related guards pass: pipefail-grep-q, retired-runner-images,
  docs-hub-checkout-repo, shellcheck-version, builddate-stamped,
  mail-addresses and compliance-claims.
