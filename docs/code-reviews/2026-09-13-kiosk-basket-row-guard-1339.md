# Review: e2e guard for kiosk-mode basket row count (ut-docs#1339)

**Branch:** `test/1339-kiosk-basket-row-guard`
**Diff:** `e2e/tests/sale-screen-213.spec.ts` only (+~90 lines, no other files)
**Complexity:** easy — Dev at Sonnet (inline), Review at fresh-context Sonnet subagent
**Verdict: safe to merge, no blocking findings.**

## What shipped

`sale-screen-213.spec.ts`'s existing `>=4 basket lines visible without
scrolling at 1280x800` assertion only covers non-kiosk mode. ut-docs#1314's
review (`docs/code-reviews/2026-08-30-basket-item-name-column-width.md`)
found that fix's stacking-qty-above-discount trade-off specifically bites
under `body.kiosk` (its 2.1rem `.qty-input`/`.disc-input` min-height), and
flagged that no e2e guard existed for a kiosk-mode row-count regression.

This adds one: a new `test.describe('kiosk-mode basket layout floor
(ut-docs#1339)')` block at 1024x600 (the documented kiosk-hardware floor),
reusing the file's own `CODES`/`scan()`/`resetBasket()`/`watchConsole`/
`waitForStableLayout` helpers, asserting `>=2` fully-visible basket rows.

## Key design decision, and why

`body.kiosk` is driven entirely by a Go template func backed by an atomic
bool set ONCE at process start from `UT_KIOSK` env (`internal/pages/init.go`
→ `httpx.InitKiosk`) — not by any dynamic settings toggle. None of this
suite's 5 webServer projects (`e2e/playwright.config.ts`) set `UT_KIOSK=1`,
so `body.kiosk` is never true in any existing e2e run today, and there was
no existing toggle pattern to reuse despite the card's own text assuming
one ("however this suite toggles `body.kiosk` for other specs — check
`settings-osk.spec.ts`/similar for the existing pattern"). Confirmed by
reading `settings-osk.spec.ts` directly: it has no such pattern.

Rather than add a 6th webServer+project pair (a real `UT_KIOSK=1` server)
for one CSS-only regression guard, this reuses a technique already present
in the SAME file: the `'products grid keeps its floor under vertical
pressure (OSK padding)'` test injects the `osk-padded` class via
`page.evaluate` rather than driving the real OSK toggle. `body.kiosk`'s CSS
rules apply identically regardless of whether the class is server-rendered
or script-added (confirmed: no `web/public/*.js` file reads/checks the
`kiosk` class — grepped, zero hits), so this is a faithful, lower-cost
substitute for this specific assertion.

## Numbers: measured today, not copied from the 2026-08-30 review

That review measured a kiosk floor of **1** fully-visible row at 1024x600 —
but using six long-named "Cheddar Cheese 400g" lines on a real `UT_KIOSK=1`
server. Re-measuring against current `main` with THIS suite's own
`CODES`/`scan()` (five shorter-named demo items) gives a different, real
number: **2**, identically for kiosk and non-kiosk at this viewport, run 3x
independently by both Dev and Reviewer with byte-for-byte stable results.
The assertion uses `>=2` — today's actual measured floor — not the review's
stale `1`, per the card's own AC ("assert the CURRENT kiosk-mode row count
as a floor... so a FUTURE regression below today's kiosk numbers is
caught"): asserting the review's `1` here would silently pass a real
regression from 2 down to 1.

## Independent review — what was actually run, not just read

A fresh-context Sonnet subagent, isolated via `Agent(isolation: "worktree")`,
independently:
- Ran `go build ./...` / `go vet ./...` (clean, expected — no Go files
  touched).
- Ran the new test for real (`playwright test --project=default -g
  "kiosk-mode basket layout floor"`) — passed.
- Instrumented and re-ran 3x, independently reproducing the exact measured
  value (`fullyVisible: 2`, identical box/row geometry every run) —
  confirming the diff's own claim rather than trusting it.
- **Non-vacuity check**: temporarily removed the `classList.add('kiosk')`
  call and re-ran — still measured 2 (confirms the diff's own disclosure
  that kiosk/non-kiosk currently coincide at this viewport/CODES, not a
  hidden bug). Then restored the class-add and bumped the threshold to
  `>=3` — test correctly **failed**, proving the assertion is live, not a
  tautology.
- Checked CSS cascade order-sensitivity (script-appended class vs.
  server-rendered order), `!important` usage near the relevant rules, and
  any JS reading `body.classList`/`className` for `kiosk` — none found;
  no cascade discrepancy.
- Checked for cross-test state leak (Playwright gives every `test()` a
  fresh page/context by default — confirmed via `playwright.config.ts` and
  `fixtures.ts` — so unlike `settings-osk.spec.ts`'s real server-side leak
  precedent, a client-only `classList.add` cannot leak across tests here).
- Restored its worktree to the exact committed state before finishing
  (verified `git status --porcelain` empty).

Verdict: **safe to merge**, no blocking issues.

## Findings — fixed

Two non-blocking suggestions from the independent review, both applied
before this commit:
1. Added `document.body.classList.remove('kiosk')` before `resetBasket`,
   for stylistic symmetry with the file's existing `osk-padded` test
   (functionally unnecessary — confirmed no cross-test leak — but keeps
   the file's two synthetic-class tests reading the same way).
2. Added a note at the assertion itself (not just the `describe`-level
   comment) that today's kiosk and non-kiosk floors coincide at this
   viewport/CODES, so a future reader hitting a failure here understands
   this guard's actual job (a forward-looking floor, not currently a
   kiosk-vs-non-kiosk differential — `basket-item-name-width-1314.spec.ts`
   is what stresses that difference, via long product names).

## Verified beyond automated tests

- Full `sale-screen-213.spec.ts` file (all 8 tests) run together after the
  fix — no interference between the new test and the existing 7.
- Full `default`-project e2e suite (496 tests) run once during Dev, all
  green, before handing off to review.
- Manually confirmed the assertion is not vacuous (fails when the true
  count is below the asserted floor) — done independently by both Dev and
  Reviewer.

## Explicitly deferred / out of scope

- Standing up a real `UT_KIOSK=1` webServer+project pair, so this suite
  could assert on server-rendered `body.kiosk` directly instead of a
  script-injected class. Not needed for this card (the injection technique
  is faithful for this CSS-only assertion, verified above), but would be
  the right foundation if a future card needs to test something the
  class-injection technique can't reach (e.g. server-side behavior that
  actually branches on `UT_KIOSK`, not just CSS).
- Re-verifying the 2026-08-30 review's original long-name/1-row numbers
  against current `main` — out of scope for this card, which guards THIS
  suite's own existing item set; `basket-item-name-width-1314.spec.ts`
  already covers the long-name wrapping case at both viewports.

## Merge

No demo/shop-name data introduced, no secret-shaped literals. Not a UI
surface change (test-only diff) — UX-guidelines and manual/help-topic
checklists don't apply. Merged with `merge_method: "merge"` (never squash/
rebase), per this repo's standing git-identity/attribution rule.
