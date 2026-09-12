# Code review: guard-price-history-sync worktree exclusion (ut-docs#2129)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2129
**Branch:** `fix/2129-guard-price-history-worktree-exclusion`
**Complexity:** easy (Dev: Sonnet inline, Review: fresh-context Sonnet subagent, isolated worktree)

## What shipped

`scripts/ci/guard-price-history-sync.sh` scans the whole working tree
(`grep -rnE ... --include='*.go' .`) for a real caller of
`AppendPriceHistoryItem`/`AppendPriceHistoryVariant` outside `internal/pos`.
It did not exclude `.claude/worktrees/` (gitignored `Agent(isolation:
"worktree")` checkouts — see this repo's own "Agent worktree hygiene"
section), so any tree with agent worktrees checked out got a false
failure: 12 hits, every one under
`.claude/worktrees/agent-*/internal/pos/pricing.go` — copies of the same
already-excluded file, not a real new caller. Confirmed as a false positive
in the original report: the identical guard passes on a clean
`origin/main` worktree.

Fixed by adding one more `grep -v` stage excluding
`^\./\.claude/worktrees/`, with a comment explaining why (a worktree is
just a copy of this repo's tracked files at some other commit, so a
caller inside one is never a *new* caller). Also required by the card:

- **Audit of the other guards** for the same whole-tree-scan pattern —
  found none. Every other `scripts/ci/guard-*.sh` that recurses scans an
  explicitly named, non-root subdirectory (`internal`,
  `SEARCH_DIR="internal/pages"`, `web/...`, `e2e/tests`, named files) that
  cannot reach `.claude/worktrees/`; the two Go-based checkers
  (`checkpagehttperror`, `checkhelptopics`) both default to/are called
  with `internal/pages` explicitly, never a repo-root walk.
- **A self-test fixture** in `guard-price-history-sync_test.sh`: a decoy
  caller planted under a dedicated, uniquely-named
  `.claude/worktrees/zz-guard-test-fixture-2129/` directory (never
  colliding with a real, live `Agent(isolation: "worktree")` checkout),
  asserting the guard now ignores it — cleaned up via an absolute-path
  `rm -rf` on that exact directory, both inline and in the test's `EXIT`
  trap (never a glob).

## Independent review (fresh-context Sonnet subagent, isolated worktree)

Ran the real test suite, independently re-verified the TDD claim, and
independently redid the audit of the other guards rather than trusting the
author's claim.

**Verdict: PASS — merge-safe. No changes made.**

### TDD re-verification (exact output)

Reverted `guard-price-history-sync.sh` to `main`'s version only, re-ran
`guard-price-history-sync_test.sh`: the new worktree-decoy case **failed**
(`❌ FAIL: expected guard to ignore a caller planted under .claude/worktrees/
… but it rejected it`, with the guard's own rejection message naming the
decoy file), 1 of 8 cases failed, exit 1 — every other case still passed,
confirming the failure is isolated to exactly the new case. Restored the
fix, re-ran: all 8 cases passed, exit 0, working tree clean.

### Audit re-verification

Independently re-grepped every `scripts/ci/*.sh` for `grep -r`/`find`
recursion and traced each target variable to its definition, plus checked
both Go-based checkers' call sites directly. Agreed with the author's
conclusion: `guard-price-history-sync.sh` was the only guard scanning from
repo root, so the only one that could ever reach `.claude/worktrees/`.

### Other findings — all checked and confirmed sound, no fix needed

- **Exclusion correctness**: empirically confirmed (not assumed) that GNU
  grep (this environment's and CI's `ubuntu-latest`) always emits
  `./`-prefixed paths when the scan target is literally `.`, so
  `grep -v '^\./\.claude/worktrees/'` reliably matches every hit under
  that path.
- **False-negative risk**: not real — `.claude/worktrees/` is
  gitignored and tool-managed (documented in this repo's own CLAUDE.md);
  there's no legitimate reason production Go source would ever live
  there.
- **Cleanup trap robustness**: the `EXIT` trap fires on every exit path
  (including a `set -e` abort) and is idempotent (guarded by
  `-n && -d`) whether or not the explicit `rm -rf` earlier in the script
  already ran.
- **Decoy fixture shape**: byte-for-byte the same shape as the existing
  `RealCaller` fixture, and its call signature verified to match the real
  `internal/pos/pricing.go` function signature — a faithful reproduction
  of the original bug's shape, not a synthetic shortcut.
- **No stray artifacts**: `git status --porcelain` clean and
  `find .claude/worktrees -maxdepth 2` empty after a full test run.
- **Shellcheck**: not installed in either the author's or the reviewer's
  environment; both independently hand-read the diff for the patterns
  shellcheck would flag (unquoted expansions, glob-in-`rm`, unused vars)
  and found none. CI's own `guard-shellcheck-version.sh`-pinned shellcheck
  run is the actual gate for this and will run on the PR.

## Verification

- `bash scripts/ci/guard-price-history-sync_test.sh`: 8/8 cases pass,
  including the new worktree-decoy case, on both the author's and the
  reviewer's independent runs.
- `bash scripts/ci/guard-price-history-sync.sh` on the real, unmodified
  tree (with the real live `.claude/worktrees/agent-*` checkouts this
  session's own review subagents created present on disk): passes clean.
- Confirmed the real, in-use `.claude/worktrees/agent-*` directories were
  never touched by any part of this fix or its test.

**Safe to merge.** No UI surface, no manual/help-topic implication, no
deferred follow-up items — the audit requirement is satisfied with a
documented "found none" result rather than a silent skip.
