# Review: a starved goreleaser no longer cascades; the retry checks the kept tag (ut-docs#5209)

Date: 2026-10-30 · Branch: `ci/5209-release-starved-goreleaser` · Built by
Sonnet 5.5, reviewed by Opus 5.5 (fresh context, own worktree,
mutation-tested, checked against real release runs via the API).

## What shipped

- `.github/workflows/release.yml`: `windows-installer`, `macos-app`,
  `android-app` and `verify-versions` also skip when `goreleaser` was
  `cancelled` (a starved runner, or a timeout); `verify-versions` also when
  `android-app` was. A goreleaser *failure* still runs them, as before.
- `scripts/ci/release-runner-retry.sh`: before a PRERUN re-run, `main()`
  resolves the release tag (`release_tag`: `HEAD_BRANCH` for a tag push,
  else the tag named in `cleanup-orphaned-tag`'s keep warning), checks its
  shape (`^v[0-9A-Za-z._-]+$`) and that it exists (`tag_exists`,
  `git/ref/tags/<tag>`). Gone or unconfirmed → no re-run, with a warning
  whose advice fits the trigger (dispatch: fresh release; push: re-run by
  hand). A starved `prepare` made no tag, so it re-runs with no check. The
  jobs list is fetched once and reused. `keep-tag` and the post-publish
  RERUN path are unchanged.
- `.github/workflows/release-runner-retry.yml`: passes `EVENT` and
  `HEAD_BRANCH` through `env:`; permissions unchanged (`contents: read`).
- Tests (`release-runner-retry_test.sh`, 45 → 66 checks): t1–t10 behaviour
  cases, two `decide` cases, wiring checks for the new `if:` guards and for
  the keep-warning phrase the parser depends on.
- Docs: `docs/arch/desktop-app.md`, script and workflow headers.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A dispatch whose `prepare` starved is no longer retried: no tag exists and cleanup never ran, so the tag check fails. | **Fixed**: a starved `prepare` re-runs with no tag check (t9). |
| 2 | minor | On a tag-push run the skip warning advised a fresh dispatch and deleting the tag, but CI never deletes a pushed tag. | **Fixed**: push wording says "Re-run failed jobs" by hand (t8c). |
| 3 | minor | Residual: a transient API error, or an unreadable keep annotation, leaves a kept tag with no re-run. The warning names the manual step. | **Accepted**; the version-burn residual is tracked on ut-docs#5211 (card finding 3, which needs `contents: write`, kept out of this change). |
| 4 | nit | Removing the keep clause from the parse regex survived every test. | **Fixed**: t10 feeds a "did not publish" line without it; the mutant now fails. |
| 5 | nit | c1/c2 `decide` cases also pass on the old code (decide is unchanged); wiring checks are substring greps. | Accepted: they document the new skip shape; t-cases are the regression catchers. |
| 6 | nit | The `cancelled` guard also covers a goreleaser timeout, not only starvation. | Accepted, harmless: publish needs goreleaser `success`; `decide` still classes a timeout as real. |

## Verified beyond the unit tests

- Real runs via the API: a starved job concludes `cancelled` with the
  "was not acquired by Runner" annotation and does not set the run's
  `cancelled()` (so the cascade was real); cleanup started 3 s after a
  starved cancel in run 40882053145 (the race is real); a step `::warning::`
  is readable from `check-runs/{id}/annotations` with exactly the
  `Release <tag> ...` text; `head_branch` is the tag on a tag push and
  `main` on a dispatch.
- TDD re-verified by the reviewer in a separate worktree: parent script and
  workflows with the new test → 15 failures; with the change → all pass.
  The fix-ups were re-verified inline: the pre-fix-up script fails t8c, t9,
  t10; the keep-clause mutant fails t10.
- Gates: `release-runner-retry_test.sh` (66/0), `release-marketplace-key_test.sh`,
  `release-macos-nonblocking_test.sh`, `release-job-timeouts_test.sh` pass;
  `shellcheck scripts/ci/*.sh` 0 issues; both workflows parse as YAML.

## Verdict

Safe to merge. The live check is the next starved release run.
