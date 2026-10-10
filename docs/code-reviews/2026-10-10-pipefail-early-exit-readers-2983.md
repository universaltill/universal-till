# Review: pipefail early-exit reader guard covers workflows, -l/-m and head (ut-docs#2983)

- **Date:** 2026-10-10
- **Branch:** `fix/2983-pipefail-early-exit-readers`
- **Card:** universaltill/ut-docs#2983 (follow-up from the #2946 review)
- **Author lane:** `lane:cloud-41` (built on Opus 5.5, `complexity:medium`)
- **Reviewer:** an independent Fable subagent, working in a detached worktree

## What shipped

- `scripts/ci/guard-pipefail-grep-q.sh` now flags a single `|` into any reader that exits early: `grep` with `-q`, `-l` or `-m` in any flag cluster (also `--quiet`, `--silent`, `--files-with-matches` and `--max-count`), and `head` in any form. Before, it flagged only `grep -q`.
- It also scans GitHub Actions `run:` steps that execute under pipefail. A new Go helper, `scripts/ci/pipefailruns`, parses `.github/workflows/*.y{a,}ml` with yaml.v3 and resolves each step's effective shell in this order: the step's own `shell:`, then the job's `defaults.run.shell`, then the workflow's.
  - Under pipefail: exactly `bash` (Actions runs `-eo pipefail`), a custom shell string that names pipefail, or a body that runs `set … -o pipefail`.
  - The helper writes each matching body to a temp file. The bash guard scans those files with the same regex and reports `file=<workflow>,line=<real line>`.
  - Steps on the implicit default shell (`bash -e`, no pipefail) are not flagged.
- A same-line `pipefail-reader:allow <reason>` exempts a reviewed line.
- Every existing hit was rewritten:
  - In 4 workflows, `printf | grep -q` and `security find-identity | grep -q` became a here-string. In `release.yml` the `security` output is now captured first.
  - In 5 scripts, `| head -1` / `| head -n 1` became `sed -n 1p`, which reads all of its input.
  - In `guard-release-notes.sh`, an awk `{exit}` fed from `printf "$CONTENT"` was rewritten. The regex can't see this case; the guard's header now says so.
- `gopkg.in/yaml.v3` moved from indirect to direct in `go.mod`. `go.sum` is unchanged.
- The `ci.yml` step names were updated.

## TDD evidence

- The new bash test cases failed before the guard change: 28 FAILs, covering every new reader form and all workflow cases. The reviewer reproduced this by running the new test against the guard on `origin/main`.
- The Go extractor tests were written first. They failed because `extract`/`run` were undefined.
- Before the rewrites, the new guard flagged all 12 real hits at the right lines (for example `android-ci.yml:85` and `release.yml:504`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `go.mod`: yaml.v3 had its `// indirect` comment removed but stayed in the indirect block, so the next `go mod tidy` would churn the file. | **Fixed.** Moved into the direct block. `go mod verify` and `go build` pass. |
| 2 | minor | `pipefailSet` missed `set -o errexit -o pipefail` and `set -e; set -o pipefail`. | **Fixed.** Broader regex. Comment lines are still excluded. Test cases added. |
| 3 | minor | No reviewed-exception marker, unlike the sibling guards. | **Fixed.** `pipefail-reader:allow`, with a test case. |
| 4 | minor | Regex false negatives: a flag with a separate argument before `-q` (`grep -A 1 -q`), and `timeout`/`VAR=` prefixes. These come from the old guard's shape. | **Accepted.** None in the repo today. This is a lint, not a parser. |
| 5 | nit | Folded `>`, multi-line plain and quoted `run:` bodies are reported at the body's first line. | **Documented** in the code. No workflow uses them. |
| 6 | nit | Expression shells (`${{ matrix.shell }}`), aliases and composite actions are not scanned. | **Accepted.** None exist in the repo yet. |
| 7 | nit | The test uses GNU `sed -i` with `\n` in the replacement. | **Accepted.** Same precedent as `guard-competitor-naming_test.sh`. CI is ubuntu. |

The reviewer confirmed that every rewrite behaves as before:
- A here-string adds the same trailing newline as `printf '%s\n'`.
- `sed -n 1p` gives the same output as `head -1`.
- The two awk forms are equivalent.
- `identities=$(…) || identities=""` is safe under `set -e`, and a failing `security` still reaches the error branch.

## Verified

- `guard-pipefail-grep-q.sh` passes on the tree, and its `_test.sh` passes.
- `go vet` and `go test ./scripts/ci/pipefailruns` pass. `gofmt` is clean. `golangci-lint run ./...` reports 0 issues. `go build ./...` passes.
- `shellcheck` reports 0 issues on every touched script.
- These pass: `guard-release-notes_test.sh`, `windows-signing_test.sh`, `ios-testflight-workflow_test.sh`, `guard-adr-plugin-taxonomy(.sh|_test.sh)` and `guard-adr-taxonomy-drift(.sh|_test.sh)`, the latter against the sibling ut-docs checkout.
- Every workflow still parses as YAML.
- Full `go test ./...` passes.
- No UI or shop-owner-visible surface changed, so there is no help topic or screenshot to update.

## Verdict

Safe to merge.
