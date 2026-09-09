# Review: lang-pack-drift actionable follow-up-PR signal (ut-docs#1857)

**Date**: 2026-09-09
**Card**: universaltill/ut-docs#1857 — "A core merge adding en.json keys can
leave both language packs red, and only the next unrelated lane finds out"
**Complexity**: medium
**Reviewer model**: fresh-context Opus subagent, isolated worktree (per this
card's `complexity:medium` tier — see `scrum-master` skill's model routing)

## Problem

`lang-pack-drift` (ut-docs#299/#669) already advisory-warns on a core PR
touching `web/locales/en.json` and blocks `push: main` if an external
`ut-plugin-language-{de,es}` pack has fallen behind. It worked as designed
for ut-docs#1837's PR, and the merging lane still missed the follow-up: the
`::warning::` said only "a language pack is missing key(s) added here --
see the job summary", generic enough to scroll past without opening the
summary that actually named the exact keys.

## What shipped

- `scripts/ci/check-lang-pack-drift.sh`: tracks which pack repo(s) failed
  (`FAILED_REPOS`, covering both the fetch-failure branch and the
  check-script-failure branch) and, on overall failure, prints a new
  greppable block naming each failed pack's GitHub URL.
- `.github/workflows/lang-pack-drift.yml`: the advisory step now captures
  the script's output to a file (still also echoed to the console log and
  appended to `$GITHUB_STEP_SUMMARY`, unchanged in content/order), greps it
  for the pack URLs the new block prints, and folds them directly into the
  `::warning::` text — falling back to the old generic wording if none are
  found. Also added a "lang-pack-drift guard self-tests" step before the
  real check (mirrors each pack repo's own `ci.yml` pattern), and added the
  new test file to the `pull_request: paths:` trigger list.
- `scripts/ci/check-lang-pack-drift.test.sh` (new): self-tests via
  throwaway fixture pack repos over a local HTTP server, pointed at by the
  real script's existing `UT_RAW_BASE`/`UT_API_BASE` overrides. Each
  fixture's real `check-key-drift.sh` comes from a local sibling checkout
  if present, else a live fetch from that pack's own `main` (works on a
  bare CI checkout with no sibling repos). 4 cases: both packs in sync,
  only one drifts (block names only it), both drift (names both), one
  unreachable (fetch-failure branch, still named).
- `CLAUDE.md`: updated the `lang-pack-drift` bullet for the enrichment and
  the new self-test file.
- `ut-docs/.claude/skills/scrum-master/SKILL.md` (separate repo, separate
  commit): noted the enrichment next to the existing "own the pack
  follow-up" rule.

## Independent review (Opus, isolated worktree) — 1 HIGH, 2 MEDIUM fixed, 3 MEDIUM/LOW deferred, rest confirmed clean

**HIGH, fixed — the documented "fall back to generic wording" path was dead
code; when it would fire, the step aborted with no annotation at all and a
red PR check.** Actions' `shell: bash` is `bash -eo pipefail {0}`. A
`grep -oE` with no match exits 1; under `pipefail` that failure propagates
into the `PACK_REPOS="$(...)"` assignment, and `errexit` kills the step on
that exact line — before the fallback `else` branch, before the
force-`status=0` advisory logic. Reviewer reproduced this byte-for-byte
against the real run-block logic (case with no matching URL: step exits 1,
zero output, `main` blocking-path semantics leak onto the advisory `pull_request`
path). That is a strictly worse outcome than the pre-#1857 behavior — no
signal at all, plus a check gone red on a PR it must never block. Fixed:
wrapped the `grep` in a subshell with `|| true`
(`PACK_REPOS="$( (grep ... || true) | sort -u | ...)"`), independently
re-verified against a synthetic no-match case under real
`bash -eo pipefail` semantics — now correctly falls through to the generic
wording and force-exits 0.

**MEDIUM, fixed — the identical errexit-on-empty-grep pattern in the new
test's own assertion helper.** `assert_fail_repos` (the file runs under
`set -euo pipefail`) had the same shape; the reviewer's live revert
reproduced it exactly: one `ok` line printed, then a bare exit 1 with no
`FAIL`/want/got diagnostics and cases 2–4 never running — the diagnostic
block that exists specifically to tell a future maintainer what broke was
itself unreachable in the situation it was written for. Same `|| true`
fix, re-verified: a real revert of the fix under test now prints all three
expected `FAIL ... want: ... got: (empty)` lines before failing the suite.

**MEDIUM, fixed — the drift script's own console-log output disappeared.**
The old code piped straight to `tee`, so output reached both the console
log and the step summary. The new capture-to-file version only appended to
the summary, leaving the job's own console log bare on a real failure —
regressing the one place someone landing on a red `push: main` run
normally looks first, in a card whose whole point is making this signal
*harder* to miss. Fixed: `cat "$RUN_OUTPUT"` added back before the summary
append.

**MEDIUM, fixed — hardcoded `/home/user/...` absolute paths in the new
test's local-checkout lookup.** Not portable to any real dev machine's
layout. Fixed: resolved relative to the repo's own root (already `cd`'d to
at the top of the real script) — `../<repo>` (a flat sibling layout) and
`../universaltill/<repo>` (this pipeline's anonymous-read layout) — both
verified to still resolve correctly.

**MEDIUM, fixed — the test leaked one orphaned `python3 -m http.server`
process per case, permanently.** `( cd X && python3 ... & echo $! )`
captured the wrapper subshell's pid, not python3's — `kill "$server_pid"`
was killing the wrong (already-gone) process every time. Reviewer counted
41 live orphans across their run plus mine before reaping them by hand.
Fixed: dropped the `cd`-wrapping subshell in favor of `python3 -m
http.server --directory "$server_root" & server_pid=$!` (Python 3.7+'s
`--directory` flag), which backgrounds python3 directly so `$!` is its
real pid. Re-verified: 0 leaked processes (`ps aux | grep http.server`)
after a full 4-case run.

**MEDIUM, deferred to Backlog (ut-docs#1866) — the self-test's live
pack-script fetch has no fallback if unreachable, and now runs on every
`push: main` with no `paths:` filter.** A transient
`raw.githubusercontent.com` outage would fail the self-test step and red-X
`lang-pack-drift` on a push, contradicting the trigger's own stated intent
that external pack flakiness must never block. Mitigated today by
`--retry 3` and by this not being a required status check; not urgent
enough to block this card, but tracked rather than left unremarked per the
reviewer's explicit recommendation.

**LOW, fixed (cosmetic) — CLAUDE.md and a fixture-mode comment didn't
mention/overstated the live-fetch fallback and the "unreachable" fixture's
actual scope.** Both corrected.

**LOW, accepted as-is:** `if: always()` on the self-test step (matches the
cited pack-repo `ci.yml` precedent exactly; buys coverage for a
checkout-failed or cancelled run, not "step after a failure" since it's
first); the fetched fixture script's fixed, non-removed temp path
(non-issue on an ephemeral CI runner or this container).

**Confirmed correct by the reviewer, no changes needed:** `permissions:
contents: read` unchanged, no `pull-requests: write` added, no new secret
exposure; diff is CI-tooling + docs only (confirmed via `git diff --stat`
— no `.go`, no `internal/pages`, no `web/help/`, no `web/locales/`), so the
UX-guidelines and help-topic checks are legitimately skippable; no missing
`mkdir -p` anywhere new code writes a file; no client/shop names or
secret-shaped literals in fixtures.

## TDD re-verification (independent, isolated worktree)

Reviewer reverted `scripts/ci/check-lang-pack-drift.sh` to its pre-#1857
parent while KEEPING the new test file, re-ran the suite: the "both packs
in sync" control case still passed; the three drift/unreachable cases each
failed with `got: (empty)` — the old script prints only "one or more
language packs have drifted from core" and never emits a follow-up-PR
block, on either failure branch. Confirms the test pins the new behavior,
not an unrelated regression. Restored the fix, re-ran: all 4 pass. I then
independently re-ran the same revert/restore myself after applying the
review's own fixes on top, to confirm they didn't disturb this property —
still 4/4 pass on the fixed version, and the two `|| true` fixes were
separately verified (above) to make the *assertion helper itself* survive
a revert cleanly, which the original version did not.

## Verification beyond the automated suite

- `bash -n` on both shell scripts; `python3 -c "import yaml; ..."` on the
  workflow file — every iteration, before and after the review fixes.
- `bash scripts/ci/check-lang-pack-drift.test.sh` run directly (not just
  by the reviewer): 4/4 pass, both with local sibling checkouts present
  and with them temporarily moved aside (forcing the live-fetch fallback
  path) — confirms the bare-CI-checkout case genuinely works, not just in
  theory.
- Ran the real (unmodified-input) `check-lang-pack-drift.sh` against this
  repo's actual current `web/locales/en.json` and the real, current
  `ut-plugin-language-{de,es}` `main` branches: correctly reports real,
  pre-existing drift (5 missing keys + 1 orphan key per pack, unrelated to
  this change) and the new follow-up-PR block correctly names both real
  pack repos.
- Reproduced the HIGH finding and its fix under real `bash -eo pipefail`
  semantics (Actions' actual shell), not just prose reasoning.
- `ps aux | grep http.server` before/after a full test run: 0 leaked
  processes post-fix (was 41 cumulative pre-fix, all reaped).
- `git diff --stat` against `main`: confirmed CI-tooling + docs only, no
  Go/UI/locale/help-topic files touched — `go build`, UX-guidelines review,
  and help-topic update are correctly not applicable to this diff.

## Safe-to-merge verdict

Yes, after the fixes above. The independent review's most valuable
contribution was refusing to accept the diff's own comments at face value
— the workflow explicitly claimed "a missing enrichment must never turn
into a missing signal," and the review proved, by reproducing Actions'
actual shell semantics rather than just reading the code, that the diff as
first written did exactly the opposite in a real failure mode. All
findings fixed (bar the one explicitly deferred, now tracked) and
re-verified independently after the fixes, not just re-read.
