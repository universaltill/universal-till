# Review: per-file surface record in the docs-shots manifest (ut-docs#6654, slice 1)

Date: 2026-11-22. Diff scope: `git diff origin/main...c75f746f3` (13 files) plus the review fixes below.

## What shipped

`web/help/img/manifest.json` recorded the app surface as ONE `surface_sha256` over
`web/ui/**`, `web/public/**` and non-test `internal/pages/**.go`. Every UI PR rewrote that one
line, so any two UI PRs conflicted. It is replaced by `surface_files`, a
`{relpath: {sha256}}` map in sorted path order and serialised as the manifest's last key. Each file
gets its own 3-line block, so two PRs that change different files change lines git does not see as
adjacent.

- `scripts/ci/guard-docs-shots.sh` (Python inside): builds the record with an unchanged
  fileset function. A stale record names the changed/added/removed paths (first 40). A manifest
  that still has `surface_sha256`, or has no `surface_files`, is refused with a fix hint. The print-only
  mode now prints sha256sum-format lines.
- `e2e/tests-docs/lib.js` / `write-manifest.js`: `surfaceRecord()` replaces `surfaceHash()`, and the
  algorithm string is updated.
- `scripts/ci/update-docs-shots-surface-hash.sh` (escape hatch): rewrites only the entries that
  differ, migrates a legacy manifest, and lists what it changed.
- The cross-check test compares the full per-file records (Python vs JS) and diffs them on a mismatch.
- New `scripts/ci/docs-shots-manifest-merge_test.sh` (wired into the `ci.yml` build job): real
  `git merge-tree`/`git merge` in the sandbox (adjacent files via the updater and the writer, surface vs
  topic markdown, plus a same-file negative control). The guard must pass on each merged tree.
- `run-docs-shots-guard.sh` keeps the policy unchanged: warn on `pull_request`, fail on push to main.

## Review

- Author: Sonnet 5.5. Independent review: Opus 5.5. Tester: separate agent, PASS.
- Verdict: **approve after fixes**. All fixes are in the worktree, not committed.

### Findings

- **R1 (important, fixed): test flake from SIGPIPE.** `guard-docs-shots_test.sh` and
  `update-docs-shots-surface-hash_test.sh` ran
  `PUBLIC_FILE="$(guard | awk | grep | head -1)"` under `set -o pipefail`. When `head` exits early,
  the writer can take a SIGPIPE, and the whole test then aborts with exit 141 partway through. This
  happened in review: in the TDD revert run the test stopped right before the per-file cases and
  reported 141 instead of counting failures. The fix captures the record first, then filters it with
  `awk … exit` over a here-string. `docs-shots-manifest-merge_test.sh`'s
  `grep -l … | head -1` had the same race and now uses `mapfile`.
- **R2 (nit, fixed): a conflict case was missing from the docs.** Probed with real
  `git merge-tree` on a synthetic manifest. Editing either of the last two entries conflicts with
  an add or delete at the end, because the last entry's `}` has no comma. That case is now listed with
  the other residual conflicts in the guard's MERGE-SAFE LAYOUT note. It is safe because git reports it
  as a conflict, so nothing is merged silently. The same probe confirmed the documented clean cases:
  adjacent edits, an edit next to an add in the following gap, adds in different gaps, and an edit
  next to a non-adjacent delete.
- **R3 (nit, fixed): stale wording.** The header and one comment in
  `update-docs-shots-surface-hash_test.sh` still said `surface_sha256`.
- Checked and not a defect:
  - **The exclusions are unchanged.** The sha256 of the new print-only record (sha256sum lines) is
    `4caef1ac…52bc9`, exactly `origin/main`'s recorded `surface_sha256`. The fileset and its sort order
    are therefore identical to the old guard's: routes[0]-only exclusion, testdata/, `_test.go`,
    dotfiles, `web/locales` all kept.
  - **JS and Python agree byte for byte.** `node write-manifest.js` reproduces the committed manifest
    byte for byte. After a comment edit, the updater's output is byte-identical to the writer's
    (1-line diff). Paths are all ASCII (0 non-ASCII bytes in the manifest), so UTF-16 and code-point sorting
    agree, and `ensure_ascii=False` matches `JSON.stringify`. The trailing newline is present.
  - **No stale-but-passing merge.** If a file's merged content differs from both sides, both sides
    changed it. If both refreshed, the sha lines conflict. If one side did not refresh, the record is
    wrong and the guard fails on main; it never passes. Modify/delete and rename+modify both conflict
    on the record. Two branches that change different files and so different screenshots: deterministic
    PNGs mean a screenshot affected by both sides changes on both sides, which gives a binary conflict.
  - **Legacy format.** A pre-#6654 PR that merges after this one changes the deleted
    `surface_sha256` line, which conflicts and stays visible. The guard refuses a legacy manifest,
    and the updater migrates one.
  - **Workflows.** `docs-shots-determinism.yml` triggers on `e2e/tests-docs/**`, so it runs for this PR.
    The locale-drift script and workflow read no `surface_sha256`.
  - **Stale mentions.** No `surface_sha256` left outside historical code-reviews and deliberate
    "replaced" notes. None in `web/help/`, `docs/` (outside reviews), README, CLAUDE.md or ut-docs
    `reference/`.

### Extra verification

Run in the review worktree after the fixes:

- These tests and guards all exit 0: `guard-docs-shots_test.sh`, `guard-docs-shots-cross-check_test.sh`
  (625 files), `update-docs-shots-surface-hash_test.sh`, `docs-shots-manifest-merge_test.sh` (3 clean
  merges + negative control), `run-docs-shots-guard_test.sh`, `docs-shots-locale-drift_test.sh`,
  `guard-ci-tests-sandboxed.sh`, `guard-docs-shots.sh`.
- `shellcheck scripts/ci/*.sh`: 0 issues.
- **TDD re-verified.** Setting `surface_added = []` in the guard turned `guard-docs-shots_test.sh` red
  (3 false negatives on new route fixtures, exit 1 after R1). Restoring it turned the test green.

Pre-flight: help topic n/a (no shop-owner-visible change; the manifest is CI metadata);
reference/ n/a (no ut-docs page describes the manifest format); README n/a (not mentioned);
en.json/meta n/a; ADR n/a (the tooling stays within ADR-0003/#324); review record updated (this file);
follow-ups deferred → ut-docs#6654 checklist (below).

## Deferred (ut-docs#6654, item 3 follow-ups)

- An update-branch watcher for UI PRs that are behind but not conflicted, consistent with the
  owner's no-rebase-when-behind rule.
- A short-lived-PR / integration-branch rule in the `dev` and `scrum-master` skills, with a skill eval
  (#2532).
- Re-measure manifest conflicts two weeks after merge, then decide items 2 and 4 of #6654.

## CI round 1 (2026-11-22)

`build` failed on `guard-pipefail-grep-q.sh`: two new pipes into `head`
under `pipefail` (`docs-shots-manifest-merge_test.sh:35`,
`guard-docs-shots-cross-check_test.sh:52`) — the same SIGPIPE class as R1,
which the review fixed elsewhere but missed here. Fixed by capturing the
output first and filtering with `awk` / `sed -n` over a here-string. Then
every single-line `bash scripts/ci/...` step in `ci.yml`'s `build` job (172)
was run locally and passed, plus shellcheck (0 issues).
