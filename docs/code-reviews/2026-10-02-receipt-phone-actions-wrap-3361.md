# 2026-10-02 — Post-sale receipt action row clips at phone width (ut-docs#3361)

## What shipped

`web/ui/partials/receipt.html`: at `max-width: 480px` (the app's existing
phone breakpoint), `.actions` now wraps (`flex-wrap: wrap`) and each `.btn`
inside it gets `flex: 1 1 auto`, so the three post-sale receipt buttons
(Print, New Customer, ↩ Refund) reflow onto two rows instead of the last
one clipping off the right edge inside `.pos-container`'s
`overflow: hidden`. No touch-target size change (still `min-height: 3rem`).

`e2e/tests/phone-layout-sweep-3297.spec.ts`: new guard (360px and 440px)
that drives a real sale through the tender flow and asserts every
`.receipt-wrap .actions .btn` stays within `.pos-container`'s own
bounding box on both edges (catches a left-edge clip in RTL too). This
view is an htmx swap into `#basket`, not a GET route, so it can't join the
existing all-routes sweep in the same file.

## Rescoping note (BA, same session)

The card's acceptance named a "Journal" button; no button by that name
exists in this view (the three real buttons are Print / New Customer /
Refund). Reproduced the real, reported behaviour instead: at 360px the
rightmost button (↩ Refund) clipped — right edge at x≈369 against a
360px viewport. Rescoped and documented on the issue before building.

## Review

Independent review by a different model (Opus 5.5; author was Sonnet,
`complexity:easy`), in an isolated worktree off the pre-review WIP commit.

**TDD claim re-verified by the reviewer directly** (revert CSS → run →
restore → run): 360px test fails against the unfixed CSS with the exact
reported clip (`"↩ Refund" right=369… containerRight=338.75`,
`Expected: <= 339.75`), 440px passes throughout; both pass once the CSS
fix is restored. Re-verified a second time after finding 1 below.

**Findings:**
1. *(fixed)* The original assertion compared each button's right edge
   against the raw viewport width, not the real clipping ancestor
   (`.pos-container`, which has `overflow: hidden` and sits inside the
   viewport with its own padding) — a button could still clip visibly in
   the gap between the container's edge and the viewport edge and the
   test would have missed it. Tightened to compare against
   `.pos-container`'s own `getBoundingClientRect()` on both the left and
   right edge (the left check is what would catch an equivalent clip in
   an RTL locale). Re-verified: still fails against the unfixed CSS
   (now against the tighter `339.75` bound) and passes with the fix.
2. *(accepted, not fixed — noted here)* Before this fix, at 440px all
   three buttons already sat on one row but shrank until "New Customer"
   wrapped inside its own button (three ~66.5px-tall buttons). After the
   fix, `flex-wrap` sizes buttons by their natural width first, so Refund
   now drops to its own full-width second row at 440px too (three
   single-line 51px buttons: two on row one, Refund alone on row two).
   This is a visible layout change on the reported width, not only at
   360px — judged an improvement (no mid-word button wrapping), and
   within what the card asked for ("all buttons fully visible"), so
   accepted as part of this fix rather than treated as scope creep.
3. `.actions .btn` scoping checked safe: `receipt.html` is the only
   template defining `.actions`, and its one caller is
   `renderReceipt` (`internal/pages/pos_api.go`). No other class token
   collides (`modifier-actions` etc. don't match the `.actions` selector).
4. No state leak: the new test resets via `/api/pos/reset` before and
   after, matching `phone-sell-3059.spec.ts`'s convention.
5. No overlap with the fixed bottom bar: `elementFromPoint` at each
   button's centre returns the button itself at both 360px and 440px;
   screenshots looked at directly, nothing overlapping.

**Also checked:** `go build ./...` / `go vet ./...` clean (no Go changes);
`scripts/ci/guard-i18n.sh` clean (no new strings — RTL reads correctly,
`flex-wrap`/`flex` carry no literal left/right); `guard-e2e-fixtures-import.sh`
and `guard-e2e-no-browser.sh` clean; full `phone-layout-sweep-3297.spec.ts`
(86 tests, incl. the existing all-routes sweep at 360/440px and the 1024×600
tablet-layout checks) + `phone-sell-3059.spec.ts` (19 tests) green, 105/105,
both before and after the finding-1 fix. No help-manual topic needed
updating (`web/help/en/sell.md`, `quickstart.md` mention New Customer but
say nothing about button position/rows that would now read as stale). No
real shop/client names or secrets in anything touched.

**Verified beyond automated tests:** screenshots taken and looked at
directly at 360px and 440px (LTR) and 360px in `fa` (RTL) after the fix —
no clipping, no overlap, touch targets unchanged. Hardware note: no real
touch device in this session; verified via Playwright's emulated touch
viewport (`hasTouch`/`isMobile`) only.

**Verdict:** safe to merge.
