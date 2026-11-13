# Review: an hourly sweep deletes a release tag kept for a retry that never came (ut-docs#5211)

Date: 2026-10-30 · Branch: `fix/5211-release-orphan-tag-sweep` · Designed by
Opus 5.5 (session lead), built by Sonnet 5.5, reviewed by Opus 5.5 (fresh
context, own worktree, mutation-tested).

## What shipped

- `.github/workflows/release-orphan-tag-sweep.yml`: hourly (`23 * * * *`) plus
  `workflow_dispatch`; top-level `permissions: {}`; one job with only
  `contents: write` (delete a draft release and its tag ref), `actions: read`
  and `checks: read`; `timeout-minutes: 10`; a non-cancelling concurrency
  group.
- `scripts/ci/release-orphan-tag-sweep.sh`: sources
  `release-runner-retry.sh` and reuses its `release_tag` (the one reader of
  the keep-warning phrase). It deletes only when **all** hold:
  - the run is the **newest** Release run of any event;
  - it is a `workflow_dispatch` run, `completed`, not `success`, created
    within 72 h and last updated at least 2 h ago;
  - its latest attempt's `publish-release` did not succeed, and its
    `cleanup-orphaned-tag` left the keep warning;
  - the tag is still the one that run's `prepare` made: an annotated tag on
    the run's `head_sha`, tagged by `github-actions[bot]`, with the message
    `Release <tag>`;
  - `releases/tags/<tag>` answers 404 (nothing published), and the paginated
    releases listing shows only drafts.

  It then deletes the drafts, then the tag. Any lookup error deletes nothing,
  and a delete failure exits 1.
- `release.yml`: the tail of the keep warning names the sweep. The parsed
  prefix is unchanged, which `release-runner-retry_test.sh` pins.
  `release-runner-retry.sh`'s two "delete by hand" messages mention it too.
- `release-orphan-tag-sweep_test.sh` (36 checks, fixtures, no network or file
  writes) is wired into `ci.yml`'s `build` job.
- Docs: `docs/arch/desktop-app.md`, plus the script and workflow headers.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (lead, pre-review) | A human deletes a kept tag and re-dispatches. The new run recreates the same tag and an unpublished draft, and the older run (more than 2 h old, with the keep warning) would then delete the in-flight release. | **Fixed**: only the newest Release run is eligible. Covered by tests for a newer in-progress dispatch, a newer push run, and an out-of-order listing. |
| 2 | minor (lead) | shellcheck SC2034/SC2015 in the new test. | **Fixed**. |
| 3 | low-medium | The sweep never checked that the tag is still the one the run created. A human-pushed tag of the same name, or a seconds-wide re-dispatch race, could be deleted. | **Fixed**: the annotated tag's object sha must equal the run's `head_sha`, the tagger must be `github-actions[bot]` and the message `Release <tag>`. A lightweight tag is never touched. Tests cover each mismatch and a lookup error. |
| 4 | low | `releases_for_tag` read only the first page. | **Fixed**: `--paginate`, plus `releases/tags/<tag>` (published only). A 200 means never delete; any non-404 error skips. Tested. |
| 5 | low | An orphan from a run that is no longer the newest is never swept. | **Accepted**: by then `prepare` has already skipped that version, so nothing is lost. Documented as a manual delete. |
| 6 | nit | Follow-up test messages could read the previous case's output. The header repeated a sentence, and the fixture used the old warning text. | **Fixed**. |

## Verified beyond the unit tests

- API facts (reviewer): a re-run in progress reports the run as not
  `completed` and moves `updated_at`. `GET /releases` lists drafts for a
  `contents: write` token, and that token can delete refs and releases (as
  `cleanup-orphaned-tag` already does). `git/ref/tags/X` is an exact match,
  and `gh api` exits non-zero on 404. Job names match `release.yml`.
  `prepare` creates an annotated tag (`git tag -a … -m "Release ${TAG}"`) as
  `github-actions[bot]` on the checked-out commit, and fails if the tag
  already exists.
- Fail-safe under `set -e`: a malformed listing or a null date aborts with
  exit 1, and nothing is deleted.
- TDD re-verified by the reviewer in a separate worktree:
  - with the script moved aside, the test fails;
  - with the newest-run rule disabled, 4 tests fail;
  - each eligibility condition, the drafts-before-tag order and the
    empty-tag guard were mutated one at a time, and each mutant failed at
    least one test.
- Fix-ups: the new tests failed first on the earlier script (12 failures),
  then passed. The final test passes with no `gh` on `PATH`, so it is
  hermetic.
- Gates: the sweep test (36/0), `release-runner-retry_test.sh` (66/0),
  `release-job-timeouts_test.sh`, `guard-ci-tests-sandboxed.sh`,
  `guard-pipefail-grep-q.sh` and `guard-shellcheck-version.sh` pass, and
  `shellcheck scripts/ci/*.sh` (0.11.0) reports 0 issues. `actionlint` was not
  available; CI parses the workflow.

## Verdict

Safe to merge. The live check is the first starved release whose retry never
comes; until then the hourly run should log only skip lines.
