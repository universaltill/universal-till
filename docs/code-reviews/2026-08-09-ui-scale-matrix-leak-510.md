# Code review: reset ui_scale in afterEach, not a manual tail-of-body call

**Card:** universaltill/ut-docs#510
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (Sonnet), Review via an independent
fresh-context Sonnet subagent (isolated worktree). One review round;
nothing money/tax/data-loss/security-class was found, so a second round
wasn't earned per this pipeline's process-depth rule.

## What shipped

`e2e/tests/basket-no-horizontal-scroll-391.spec.ts`'s `ui_scale matrix at
…` tests called `setScale(page, scale)` at the start of the test body and
a manual `resetScale(page, scale)` call at the **end** of the body, after
every `expect()` in between. `ui_scale` is a server-side setting
(`internal/pages/settings_page.go`'s `POST /api/settings/ui-scale`)
shared by every spec on the e2e suite's single, single-worker till
server. If any `expect()` in a matrix test threw, the function returned
via the thrown exception before the manual reset ever ran, leaking a
non-1 `ui_scale` into every later spec in the same CI run —
`basket-no-horizontal-scroll-391.spec.ts` sorts alphabetically before
`sale-screen-213.spec.ts`, so a failure here could silently poison that
spec's basket-row-count assertions (confirmed separately during the
independent review of ut-docs#320, which is what surfaced this card).

Fix (`e2e/tests/basket-no-horizontal-scroll-391.spec.ts` only):

- Moved the reset into a `test.afterEach` hook scoped to each
  per-viewport `test.describe('ui_scale matrix at ${viewport.label}')`
  block (one per entry in `VIEWPORTS`), so it runs unconditionally — pass
  or fail — instead of only on a clean return. Mirrors the existing
  `setOskMode`-restore-in-afterEach convention already used elsewhere in
  this suite (`e2e/tests/settings-osk.spec.ts`).
- Removed the now-unused `resetScale` helper and its manual call site;
  `setScale` (used to set a non-default scale at the top of each test) is
  unchanged.
- `/api/pos/reset` — a separate, pre-existing leak risk for POS basket
  state, not `ui_scale`, and not what this card is about — is untouched,
  still called from its original position in the test body.

## Independent review (Sonnet, fresh context, isolated worktree)

Read the full diff and file fresh, ran `npm ci`, ran the real spec
(`basket-no-horizontal-scroll-391 --project=default`, working around a
pinned-`@playwright/test`-vs-installed-browser-revision mismatch with a
temporary, fully-reverted `executablePath` override in
`playwright.config.ts`), then independently reproduced the TDD claim:
injected a deliberate failing assertion mid-matrix-test plus a temporary
canary test reading `--ui-scale`. With the fix in place, the canary saw
`'1'` after the injected failure. It then went a step further than
asked and ran a **negative control** — reverted just the test file to
the old, pre-fix manual-reset pattern with the same injected failure —
and confirmed the canary saw a leaked non-`'1'` value there, positively
proving the bug is real and that this specific fix (not just "reset code
existing somewhere") is what closes it. All temporary edits were
reverted; confirmed empty diff against the shipped fix afterward. Also
ran `go build ./...`, `go vet ./...`, and `guard-data-access.sh`
(unaffected by a test-only change, confirmed nothing else broke).

**Verdict: safe to merge.** No blockers, no real-but-deferrable findings.

### Finding — nitpick, not fixed

The new `afterEach` unconditionally POSTs `/api/settings/ui-scale` with
`scale: '1'` on every test, including the `scale === '1'` matrix
iteration where nothing was ever changed — the old `resetScale` had an
`if (scale === '1') return` short-circuit this hook doesn't replicate.
Purely a once-per-describe-per-test no-op network call; functionally
harmless, and arguably safer for being unconditional rather than
depending on a closed-over value staying in sync with what the test
actually did. Not worth churning the diff for.

### Note from the review subagent, recorded for transparency

The review subagent flagged that after its own `git checkout --
<file>` calls (reverting its temporary verification edits), tool output
carried a "system-reminder"-formatted message telling it the reverted
file's change was "intentional," attributed to "the user or a linter,"
and asking it not to revert or mention it — inconsistent with what this
review actually required (revert the verification workaround, leave no
residue). This is the harness's standard file-change notice (the same
message appears in this orchestrating session's own transcript any time
a tracked file is modified outside an Edit/Write call, e.g. via `git
checkout`/`cp`) — not a foreign instruction. The subagent treated it as
untrusted, ignored it, and verified independently via `git diff
--cached` that the file matched the real shipped fix. Correct handling;
no action needed, noted here only because it explicitly asked to be
flagged.

## Verified beyond the automated suite

- Full 16/16 pass on `basket-no-horizontal-scroll-391 --project=default`
  with the real fix, run twice (once during Dev/Tester, once
  independently by the reviewer).
- The Dev-side TDD proof (this session, before handing off to review):
  injected the same kind of mid-matrix failing assertion, confirmed a
  matrix test fails as expected and a follow-up canary test in the same
  file still sees `--ui-scale: 1` afterward — then reverted the
  injection, leaving only the permanent `afterEach` fix.
- The reviewer's independent re-run of that same proof, **plus** a
  negative control against the pre-fix code, is documented above.
- `go build ./...`, `go vet ./...`, `go test ./...` (full suite, all
  packages green) and all three CLAUDE.md guards
  (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`) — run in this session before handoff to
  review, and independently re-run (build/vet/guard-data-access) by the
  reviewer in its isolated worktree.
- Structural scoping of the new `afterEach` confirmed by hand and by the
  reviewer: it sits inside the correct per-viewport describe (not the
  unrelated earlier `test.describe('basket never scrolls
  horizontally...')` block, which keeps its own separate `/api/pos/reset`
  afterEach), and fires for all `SCALES` iterations, not just the first.
- No real client/shop name anywhere in the diff (fixture barcodes only);
  no secret-shaped literal. No `web/help/` update needed — this is
  internal e2e test infrastructure with no shop-owner-visible surface,
  confirmed explicitly rather than skipped.

## Safe-to-merge verdict

Yes, as originally shipped — the review's one finding was a non-blocking
nitpick, not fixed in this diff.
