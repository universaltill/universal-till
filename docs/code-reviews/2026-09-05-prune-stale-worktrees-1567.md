# Code review: prevent stale agent-worktree re-accumulation (ut-docs#1567)

**Card:** universaltill/ut-docs#1567 — "Remove 4 stale agent worktrees (233
MB) in universal-till/.claude/worktrees — they poison repo-wide greps."

**Dev model:** Sonnet (inline, `complexity:easy`) · **Review model:** Sonnet,
fresh context, isolated worktree (per the easy-tier "different instance"
review exception — see `scrum-master`'s Model routing).

## What shipped

- `scripts/prune-stale-worktrees.sh` — removes registered git worktrees
  under `.claude/worktrees/` that are simultaneously: not locked, fully
  merged into `origin/main` (an ancestor — nothing unique would be lost),
  clean (no uncommitted changes), and past a retention window (default 3
  days, by commit time). Anything else is left alone and reported.
- `scripts/prune-stale-worktrees_test.sh` — hermetic regression test: a
  disposable bare "origin" + work clone under `mktemp -d` (trap-cleaned),
  covering 5 independent cases (removed / kept-unmerged / kept-dirty /
  kept-young / kept-locked).
- `Makefile`'s new `prune-worktrees` target wraps the script.
- `CLAUDE.md`'s new "Agent worktree hygiene" section documents the
  mechanism.
- `.github/workflows/ci.yml` runs the new test in the `build` job.

## BA/investigation finding that reshaped the task

This cloud container has no `.claude/worktrees/` directory in any of the 4
repos checked (ut-docs, universal-till, ut-cloud, ut-infra) — containers
are cloned fresh per session, so the specific 233 MB reported on
2026-09-04 no longer exists to delete. `.claude/worktrees/` was also
**already** gitignored everywhere (explicit line in universal-till/
ut-cloud/ut-infra, covered by ut-docs's blanket `.claude/*` rule). So the
actual remaining gap — and the entire scope of this change — was the
missing *prevention* mechanism against re-accumulation.

## Independent review — one high-severity finding, fixed

The review subagent didn't just read the diff; it extracted both scripts
into its own sandbox and adversarially attacked them (outside-directory
worktrees, double-invocation idempotency, several caller cwds, and
mutation testing — deliberately disabling each safety branch to see if
the test suite would still report green).

**HIGH — force-delete could bypass a worktree lock.** First draft:
```bash
git worktree remove --force "${path}" 2>/dev/null || rm -rf "${path}"
```
`git worktree remove --force` correctly refuses a *locked* worktree at the
git level (locking is how a running agent session marks its worktree as
actively in use) — but the `|| rm -rf` fallback swallowed that refusal
and deleted it anyway, for any failure reason, not just a lock. The
reviewer proved this live: created a merged+clean+old worktree, locked
it, ran the script, watched it get deleted anyway.

**Fix:** parse the `locked` field from `git worktree list --porcelain`
and skip a locked worktree before any of the other three checks even run
(new `kept_locked` counter); replaced the blind `rm -rf` fallback with a
"report and keep" path for any git-level refusal — this script now never
force-deletes past git's own protection, for any reason.

**MEDIUM — the "unmerged" test case didn't test the merge-base check.**
The reviewer's mutation test disabled `git merge-base --is-ancestor`
entirely and the suite still reported all-green, because the unmerged
worktree's commit happened to also be *young*, so the (unrelated) age
check was catching it instead. Fixed by backdating that case's commit the
same way the merged cases are, so it's independently old *and* unmerged —
re-run against the same mutation, it now correctly fails.

**MEDIUM — the test wasn't wired into CI.** Every existing
`scripts/ci/guard-*_test.sh` has a `run:` step in `ci.yml`; this one
didn't, so a regression (including the two above) would never be
re-detected automatically. Fixed: added a `build`-job step running it —
the test is fully hermetic (its own scratch repo, no network writes, no
shared state), so there's no reason not to.

**Minor (non-blocking, applied anyway):** a one-line comment on the
`git fetch` noting it fails safe under a shallow/single-branch checkout
(merge-base then errors → treated as "unmerged" → kept, never a false
delete).

Both fixes were verified two ways: the review's own live repro (locked
worktree, disabled merge-base check) confirmed the *original* bug, and
after fixing, the same two mutations were re-run against safe on-disk
*copies* of the script (never the tracked file) to confirm the updated
test suite now correctly fails on each — i.e. the regression tests
actually regression-test something.

## Verified beyond the automated test

- `go build ./...` and `go test ./...` clean (no Go files touched by this
  diff at all; run anyway per the standing gate).
- `bash scripts/ci/guard-i18n.sh` and `bash scripts/ci/guard-makefile-version.sh`
  clean (sanity — this diff touches `CLAUDE.md`/`Makefile`, neither guard's
  actual subject matter).
- Manually confirmed the real repo's own `.claude/worktrees/` was never
  created or touched by any of this testing (it doesn't exist here), and
  no stray scratch directories were left in `/tmp` afterward.
- No client/shop name or secret-shaped literal in either script.
- Backend/tooling change only — no UI surface, no user-facing string, no
  manual-topic update needed; the UX/visual/help-manual checklist is
  intentionally skipped, and the visual-check attestation doesn't apply.

## Verdict

Safe to merge. Both review findings are fixed and independently
mutation-verified; the two medium findings and the minor note are also
addressed. No items deferred.
