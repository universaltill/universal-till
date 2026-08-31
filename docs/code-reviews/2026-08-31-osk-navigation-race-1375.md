# Review: e2e `setOskMode` navigation race (ut-docs#1375)

**Date:** 2026-08-31
**Branch:** `fix/1375-osk-navigation-race`
**Files touched:** `e2e/tests/helpers.ts` only (test-helper, no production code)

## What shipped

`e2e/tests/helpers.ts`'s `setOskMode(page, mode)` called
`await page.goto('/settings')` with no wait for a prior, possibly still
in-flight navigation. This raced Playwright and threw either
`Navigation to "..." is interrupted by another navigation to "..."` or
`net::ERR_ABORTED` — reproduced in CI on
`tests/shifts-tips-osk-1272.spec.ts`, on both `universal-till` PR #688's
head and `main`'s own tip (see that PR's comments for the two
reproductions that led to this card being filed).

Fix: wait for the page to settle (`page.waitForLoadState('load')`)
before the `goto`, then wrap the `goto` itself in a retry loop (up to 3
attempts) that only retries on the two known race error shapes,
re-throwing anything else immediately. The upfront wait alone isn't
sufficient — if the prior navigation hasn't actually started yet at
that check, it reports "already loaded" and the race can still hit,
just as `ERR_ABORTED` instead; the retry loop is what actually closes
the gap regardless of *when* the trailing navigation fires.

## Verification (mine, before review)

- Reproduced the `ERR_ABORTED` shape locally with a `waitForLoadState`-only
  first attempt at the fix (1/16 repeat-each runs), which is what proved the
  upfront wait alone wasn't enough and drove the retry-loop design.
- With the retry loop in place: 30/30 passes of
  `shifts-tips-osk-1272.spec.ts` (`--repeat-each=15`), plus a clean 46/46
  across every other spec that calls `setOskMode`
  (`osk-central-guard`, `osk-decimal-admin-fields-1275`,
  `osk-signed-minus-key-1276`, `sale-screen-osk-scan-submit-1177`,
  `settings-osk`, `designer-search`, `index-keyboard-1023`,
  `shifts-tips-osk-1272`).

## Independent review (fresh-context Sonnet subagent, per `complexity:easy`)

**Verdict: safe to merge as-is.** No blocking or non-blocking correctness
findings.

- Confirmed via `git diff --stat` against the branch's pre-fix base that
  this is test-helper-only — none of `universal-till/CLAUDE.md`'s
  repository-pattern/money/i18n/kiosk-isolation guards apply.
- Checked the retry loop's bound (max 3 attempts, no infinite-loop path)
  and that non-race errors re-throw immediately on the first hit.
- Verified the two matched error strings against the installed Playwright
  version's own source, confirming the regex isn't guessing at message
  text.
- **Independently reproduced the retry path actually firing**: temporarily
  instrumented the catch block, stress-ran the spec, captured the retry
  triggering for real (twice in 16 runs, both times followed by a pass),
  then restored the diff to exactly the reviewed version (verified
  byte-identical via `md5sum`).
- Ran `shifts-tips-osk-1272.spec.ts`, `osk-central-guard.spec.ts`,
  `settings-osk.spec.ts` itself: 22/22 passed, plus its own additional
  `--repeat-each=8`/`--repeat-each=12` stress runs with no failures.
- **Nit (non-blocking, not applied):** the upfront `waitForLoadState`
  before the loop is largely redundant given the loop's own catch block
  performs the same wait before every retry attempt — the reviewer
  confirmed removing it doesn't change behavior across 20 stress runs, but
  recommended keeping it since the comment already explains the tradeoff
  honestly. Left as-is.
- **Theoretical risk raised and accepted:** `ERR_ABORTED` is a generic
  Chromium cancellation code and could in principle come from something
  other than this specific navigation race (e.g. a genuine server crash
  cancelling the request). Judged non-blocking: the retry is scoped to
  this one helper, capped at 3 attempts, and still throws loudly if the
  condition isn't actually transient — worst case is one test taking
  slightly longer before failing anyway, not a masked real bug.

## Acceptance criteria (from ut-docs#1375)

- [x] `setOskMode` no longer races a prior in-flight navigation.
- [x] `shifts-tips-osk-1272.spec.ts` passes reliably across several
      repeated runs (30/30 locally; CI will confirm at scale).
- [x] Checked other `setOskMode` callers for the same latent race — the
      fix lives inside the helper itself, so every caller (listed above)
      is protected by the same change, not just this one spec.

## Process note (orchestrator, not a code finding)

The review subagent ran in this session's shared checkout (not an
isolated worktree) and, mid-review, made a transient instrumented edit to
`e2e/tests/helpers.ts` to empirically confirm the retry path fires. That
edit landed on disk at the same moment this session's stop-hook forced an
intermediate commit, which is exactly the shared-checkout race
`reviewer`'s own `SKILL.md` describes (ut-docs#386) and recommends
`isolation: "worktree"` to avoid. Handled by taking a WIP commit at that
point, then amending it once the subagent's report confirmed the working
tree had been restored to the exact reviewed diff (checked byte-for-byte
against the pre-race diff before amending). No content was lost and the
final commit is the one described above; noting this so a future review
step spawns with `isolation: "worktree"` rather than relying on this
recovery path again.

## Verdict

Safe to merge. `merge_method: "merge"` per this repo's own convention.
