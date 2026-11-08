# Review: CI cuts the catimport/vX.Y.Z tag from catimport/VERSION (ut-docs#5882)

Date: 2026-11-08. Complexity: medium (CI/release plumbing, write-scoped token).
Built by Sonnet (Dev subagent); tests extended by a separate Tester subagent; reviewed by an independent
Opus 5.5 subagent in a detached worktree.

## What shipped
- `catimport/VERSION` (`0.5.0`, one bare `X.Y.Z` line). It is the single source for the next catimport tag.
- `scripts/ci/catimport-version-lib.sh`: one definition of "catimport changed" (the Go/go.mod/go.sum
  pathspec), of a valid VERSION (strict, single line, no CR), of numeric semver compare, and of the
  latest `catimport/v*` tag. All three scripts source it, so they cannot disagree.
- `scripts/ci/guard-catimport-version.sh` (+ `_test.sh`, sandboxed), wired into `ci.yml`'s `build` job.
  A depth-2 fetch of `GITHUB_SHA` and the `catimport/*` tags runs first. Source changes must raise VERSION
  above both the latest tag and the base's VERSION. A bump-only change must also be above the latest tag.
  A missing or malformed VERSION, a below-tag VERSION, a missing parent or no tags all fail closed.
- `scripts/ci/catimport-tag.sh` (+ `_test.sh`) and `.github/workflows/catimport-tag.yml`, which runs on a
  push to `main` touching `catimport/**`, or on `workflow_dispatch`. It refuses any ref but `main` and any
  shallow or non-main-ancestor HEAD. It creates an annotated tag on HEAD when V > L, otherwise does
  nothing or fails red. The tag is pushed without force and is never moved or deleted. The workflow sets
  `permissions: {}` and gives `contents: write` to the tag job only. No `${{ }}` reaches `run:`.
- `check-catimport-pin.sh` sources the lib and gives a new hint. Its CLI (`<pinned-version> [dir]`, exit
  2/1/0) is unchanged, so ut-cloud's `manifest-contract-guard` call
  (`bash universal-till/scripts/ci/check-catimport-pin.sh "$(go list -m …)" universal-till`) keeps working.
- `catimport/README.md` Versions section and a `CLAUDE.md` rule. On ut-cloud, branch
  `docs/5882-catimport-ci-tag` changes one `CLAUDE.md` bullet.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | high | On the push to `main`, `catimport-tag.yml` (≈30 s) tags the merge commit long before `ci.yml`'s `build` job reaches the guard, which runs after the Go tests. The guard then sees L = V and goes red on `main` after every catimport merge, including this PR's own merge ("bump-only change must raise it"). Reproduced in a throwaway repo. | fixed: when `catimport/v<VERSION>` already peels to HEAD, the guard passes ("already on HEAD"). Three new `_test.sh` cases cover a source push tagged on HEAD, a bump-only push tagged on HEAD, and VERSION's tag on the base rather than HEAD (still red). Re-verified: the first two fail against the old guard and pass with the fix. |
| F2 | low | The workflow header said the lane's tag push was refused "by the org rules in ut-docs#4735". The issue says the cloud session's permission classifier refused it ("Modify Shared Resources"). | fixed: wording corrected |
| F3 | low | The header called "Re-run failed jobs" safe without saying that re-running an old run after a newer tag exists goes red ("below the latest tag"). | fixed: the comment now names that case and says to use "Run workflow" on `main` instead |
| F4 | nit | ut-cloud `CLAUDE.md` bullet was one ~150-column line among ~78-column wrapped text | fixed: wrapped |
| F5 | low | If a third tag run arrives while one is queued, GitHub cancels the older pending run of the `catimport-tag` concurrency group. That version is never tagged, and the next bump tags a higher one. | accepted: harmless to ut-cloud, which pins any tag ≥ the change |
| F6 | low | Re-running `ci.yml` on an old `main` commit after a newer catimport tag exists makes the guard red (V < L) | accepted: only on re-runs of stale commits, and fresh runs on `main` are green |
| F7 | nit | In `catimport-tag.sh`, a malformed VERSION fails via `die` but the `tag-malformed-ok` mutant survives (the error falls through to `semver_cmp` under `set -u`) | accepted: still fails closed and tags nothing |

No secrets, real names or shop data in fixtures (`t@example.invalid`, the bot noreply address).

## Extra verification (reviewer, in a separate worktree)
- shellcheck 0.11.0 `scripts/ci/*.sh`: 0 issues (before and after fixes).
- Passing before and after fixes: `guard-catimport-version_test.sh`, `catimport-tag_test.sh`,
  `guard-catimport-module_test.sh`, `guard-ci-tests-sandboxed.sh` and `guard-pipefail-grep-q.sh`.
  `catimport-tag.yml` parses as YAML.
- TDD re-check: I neutralised the base-VERSION rule (`need = max(L, Vb)`) in
  `guard-catimport-version.sh`. `_test.sh` went red ("source change under an untagged bump on the base:
  passed, want fail"). Restored, it went green.
- PR semantics: `actions/checkout` on `pull_request` checks out the merge commit, so `HEAD^1` is the base
  tip. On push it is the previous `main` (merge method "merge", #250). The depth-2 fetch of `GITHUB_SHA`
  supplies that parent on the depth-1 checkout, which the Tester showed end to end.
- The first merge tags v0.5.0. Origin's highest tag is `catimport/v0.4.0`, and VERSION is 0.5.0, so the
  tag job will create annotated `catimport/v0.5.0` on the merge commit. Thanks to F1, the `build` guard on
  that push stays green whichever job finishes first. The Tester's simulation showed
  `check-catimport-pin.sh v0.5.0` passing, which unblocks ut-cloud once it bumps its `require`.
- Token: the repo's only ruleset targets branches, and there is no tag protection (`/tags/protection`
  404), so `GITHUB_TOKEN` with `contents: write` can push a `catimport/*` tag. DevOps confirms on the
  first real run.

Pre-flight: CI/release plumbing only. Help topic, `reference/` page and README are n/a (no till-visible
behaviour). `CLAUDE.md` is updated in both repos, and `catimport/README.md` Versions is updated. No
`en.json` key, ADR, plan or manifest change (this implements ADR-0164 §1 without amending it). Review
record: this file.

Verdict: approve after fixes. F1–F4 applied and verified (uncommitted on `ci/5882-catimport-auto-tag` and
ut-cloud `docs/5882-catimport-ci-tag`).

Deferred: none new. DevOps should watch the first `catimport tag` run on `main` for v0.5.0, then raise
ut-cloud's `require` to `catimport/v0.5.0`.

## Post-review CI fix (orchestrator)

The first PR run of `build` went red in `guard-catimport-version_test.sh`
with `fatal: empty ident name`: Actions runners have no git identity, and one
sandbox `git` call lacked the `-c user.*` flags. Both `_test.sh` files now
export a fixed `GIT_AUTHOR_*`/`GIT_COMMITTER_*` identity. That exposed a real
weakness in `catimport-tag.sh`: the caller's `GIT_COMMITTER_*` environment
overrides `-c user.*`, so the bot tagger identity is now set through
`GIT_COMMITTER_NAME`/`GIT_COMMITTER_EMAIL` on the `tag` call. The test that
checks the tagger is `github-actions[bot]` caught this. Reproduced locally
with an empty `HOME` and `GIT_CONFIG_NOSYSTEM=1`. Red before the fix, green
after; shellcheck 0 issues.
