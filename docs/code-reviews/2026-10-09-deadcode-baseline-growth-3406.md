# Code review — deadcode-baseline growth guard (ut-docs#3406)

Date: 2026-10-09 · Lane: `lane:cloud-54` · Built by Opus 5.5, reviewed by Fable (independent subagent, own worktree).

## What shipped

- `scripts/ci/guard-deadcode-baseline-growth.sh` — git-only, shrink-only check on
  `scripts/ci/deadcode-baseline.txt` against `origin/main` (env
  `DEADCODE_BASELINE_BASE`). A change that adds baseline entries **and** touches any
  other file fails: a PR may not baseline its own dead code (the universal-till#1586 /
  `DOBBeforeCutoff` shape). Allowed: baseline-only changes (plus their
  `docs/code-reviews/*.md` record), removals, and moves (an added entry whose function
  name matches a removed one; each removed entry licenses one move). Missing base ref →
  skipped with a notice.
- `scripts/ci/guard-deadcode-baseline-growth_test.sh` — fixture-repo self-test, 11 cases.
- `.github/workflows/ci.yml` `build` job: both steps after the core-neutral guard; the
  guard step fetches `origin/main` itself.
- `scripts/ci/guard-deadcode-baseline.sh`: header and failure text point at the rule.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A baseline-only PR carrying its required review record failed the guard | Fixed: `docs/code-reviews/*.md` exempt; test added |
| 2 | should-fix | Guard relied on the previous step's fetch; reordering would turn it into a silent skip | Fixed: step fetches `origin/main` itself |
| 3 | should-fix | Move heuristic was set-based: one removed name licensed many same-named additions | Fixed: multiset (one move per removed entry); test added |
| 4 | nit | Transient false positive if `main` moves between merge-ref creation and the fetch | Accepted: same exposure as the core-neutral shrink check; a re-run clears it |
| 5 | nit | CRLF baseline reads as all-new entries | Fixed: `tr -d '\r'` |
| 6 | nit | Locale-dependent `sort`/`comm` | Fixed: `LC_ALL=C` |

Also: assoc-array arithmetic now expands `${movable[...]}` explicitly; a generic-receiver
(`List[T].Old`) move case was added. Reverting that line still passes the case, so it is
defensive, not a bug fix.

## Verified beyond the unit test

- Reviewer re-verified TDD with five guard mutations (always-exit-0, no move branch, no
  untracked files, no baseline-only escape, wrong `comm` column); each failed the right case.
- Replayed the real incident: the guard run on universal-till#1586's head against its base
  (`1f20f76`) rejects `internal/pos/age_restriction.go: unreachable func: DOBBeforeCutoff`.
- All 98 `build`-job guard commands run locally: 97 pass; the one failure is
  `guard-shellcheck-version.sh` reacting to the sandbox's pip shellcheck 0.11.0. With the
  pinned 0.9.0, `shellcheck scripts/ci/*.sh` is clean.
- `guard-deadcode-baseline.sh` itself can't fully run here (no GTK headers → headless root
  set, which reports two known `internal/logging` false positives); CI's `desktop-shell` job
  runs it with headers. No Go code changed.

## Deferred

- Whether `DOBBeforeCutoff` stays baselined → ut-docs#4017 (human decision; default: remove
  until its first caller).

Verdict: safe to merge.
