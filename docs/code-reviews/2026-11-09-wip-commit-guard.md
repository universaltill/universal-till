# 2026-11-09 — WIP review-snapshot commit guard (ut-docs#5943)

**What shipped.** `guard-wip-commits.sh` + `guard-wip-commits_test.sh`, byte-identical in
universal-till/ut-cloud (`scripts/ci/`) and ut-docs/ut-infra (`scripts/`), registered in
ut-docs `scripts/check-vendored-copies.sh` (universal-till canonical). Each repo's
`commit-attribution.yml` runs the self-test, then pipes `git log --format='%h %s'
BASE..HEAD` into the guard (`if: ${{ !cancelled() }}`). A PR carrying a commit whose subject
matches `^\s*wip:.*pre-review snapshot` (case-insensitive) fails with the commits named and
the reword command. Only the PR range is scanned, so the ~18 placeholder commits already on
`main` don't trip it. ut-cloud `verify.sh` also runs the self-test; ut-docs
`reference/multi-developer-workflow.md` names the check.

**Process.** BA → Architect → Dev (Sonnet, TDD: test first, 11 failures with no guard) →
Tester (Opus) → independent review (Opus, different model from the author).

**Verification beyond the self-test.** Tester ran the YAML's own `run:` text in a clone
with a placeholder commit (exit 1), a clean commit (exit 0), and an `--amend --no-edit`
squash (exit 1). An unresolvable BASE fails closed (128). Mutation check: a case-sensitive
regex, a dropped `^`, an exact-subject match and an always-0 guard each turn the test red.
Reviewer ran all 24 `pre-review`/`^wip` subjects from every repo's `main` through the guard:
it blocks all 18 placeholders (including the suffixed ones) and passes all 6 other `WIP:`
subjects. shellcheck is clean. The repo guards pass (sandboxed-tests, mail-addresses,
runner-labels, ci-downloads, vendored copies).

**Findings.**
1. Medium (merge order). `reviewer/SKILL.md` still says to squash with `--amend --no-edit`,
   which keeps the placeholder subject, so PRs that follow it go red until the subject is
   reworded. That's the guard working. Deferred → ut-docs#5946 (a skill edit needs a full
   skill-eval run).
2. Low. `subj="${subj# }"` is redundant: the regex allows leading whitespace. Accepted.
3. Low. Subjects with no colon (`WIP pre-review snapshot`) or `wip(x):` are not caught. None
   appears in history; accepted by design.
4. Info. After a failed checkout, the `!cancelled()` steps add "No such file" noise. The
   run is red either way. Accepted.

**Pre-flight:** review record — updated; follow-up work — deferred → ut-docs#5946;
`reference/multi-developer-workflow.md` — updated; help/locales/README/ADR — n/a (CI-only, no
user-visible or architectural change).

**Verdict:** approve.
