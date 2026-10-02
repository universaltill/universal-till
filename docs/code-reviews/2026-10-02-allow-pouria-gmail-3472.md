# Review — allow pouria.teymuri@gmail.com as a commit author (ut-docs#3472)

**Date:** 2026-10-02 · **Lane:** `lane:local-pouria-teimouri` · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

Follow-up to ut-docs#3462 (everyone commits as themselves). @pouria-teimouri
wants to commit with his own GitHub-linked address `pouria.teymuri@gmail.com`
(GitHub commit search credits it to `pouria-teimouri`). The attribution guard
is default-deny for plain addresses (ut-docs#2103), so the address goes on
`ALLOWED_PLAIN_EMAILS`, with a matching `expect_pass` case. The same one-line
change lands in all three copies of the guard (ut-docs, universal-till,
ut-cloud). The 16 repos with an inline `commit-attribution.yml` don't check
plain addresses, so they need no change.

Exposure: universal-till is public, so this puts the address in public files
and, once he commits with it, in public commit history. The owner of the
address chose this knowingly on 2026-10-02 (compare ut-docs#1100).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Nit | New allowlist comment is long next to the existing entry | accepted |
| 2 | Nit | Test comment states intent (routines) beyond what the test proves | accepted |
| 3 | Info | Session memory note said CI rejects the gmail | updated after merge |

No bypass opened: the plain-email branch is the last `elif`, matching is exact
case-insensitive string equality, and unlisted plain addresses still fail.

## Verified

- Full `guard-commit-attribution_test.sh` in bash 5 (`docker bash:5`, as CI's
  ubuntu runner): all assertions pass in all three repos.
- TDD re-verified by the reviewer in isolated worktrees (ut-docs,
  universal-till): with the allowlist line removed, exactly one assertion fails
  (`pouria-teimouri's allowlisted plain email address`); restored, all pass.
- No secrets, no shop/client names.

## Verdict

Safe to merge. After all three merge, Pouria switches his git config and both
of his routine prompts (`:41`, `:41b`) to the gmail himself (#1886).
