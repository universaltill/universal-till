# Sale screen: quick-pay footer button for the preferred payment method (ut-docs#1336)

**Card:** ut-docs#1336 — product owner live UX feedback, 2026-08-30 ("we can
have more than 2 buttons next to payment"). **Complexity:** hard.
**Dev:** Fable (subagent). **Review:** Opus (isolated worktree, one round).

## What shipped

`.tender-default-footer` (Hold Sale + Payment) gets a second row below it:
one full-width **quick-pay button** for the shop's preferred/default payment
method — `⚡ <method name>`, or `⚡ Cash` when no method is configured yet.
Tapping it dispatches the exact same `/api/pos/tender` POST the payment-
overlay's own pay-grid buttons already use (`amount:0`, `method:<id>`),
completing the sale in one tap instead of Payment → open overlay → tap
method. Turns the current 3-button default view (New Sale, Hold Sale,
Payment) into 4, satisfying the product owner's ask without reintroducing
the pay-grid crowding ut-docs#1252 deliberately fixed — only ONE extra
button is surfaced, not the full method grid.

- `internal/pages/index_page.go`: new nil-safe `defaultPayMethod
  *data.PaymentMethod` template var (`&payMethods[0]` when active methods
  exist — `payMethods` is already preferred-method-first per ADR-0016 —
  else `nil`).
- `web/ui/pages/index.html`: new `.tender-quickpay` row, base `.btn` sizing
  (not the row-1 buttons' `.compact` floor) — deliberate: unlike Hold
  Sale/Payment, which only open things, this button itself completes a
  charge, so it's held to the standard touch-target floor.
- `web/public/app.css`: `.tender-quickpay` CSS + a re-added
  `@media (max-height: …)` row-share override (see "Independent review"
  below for why the threshold moved from Dev's original 720px to 710px).
- `e2e/tests/tender-panel-reachable.spec.ts`: extended the existing
  1024×600 held-sale matrix and 850×700 tablet-tier tests to also hit-test
  quick-pay, plus 4 new tests (a real tap completing a sale without opening
  the overlay first, 1280×800, 360px phone, RTL fa).
- `internal/pages/index_quickpay_test.go` (new): Go render tests for the
  labeled/preferred-reorder/zero-state branches — the e2e demo till seeds
  zero `payment_methods` rows, so e2e alone can only ever reach the
  fallback branch.
- `web/help/{en,ar,fa,tr}/{sell,payments}.md` + regenerated screenshots.

No `.go` file writes anything to disk and no path is constructed anywhere
in this diff, so neither of this pipeline's two recurring bug classes
(missing `os.MkdirAll`, a cwd-relative path where `paths.Data(...)`
belongs) apply — confirmed by reading the full diff, not just asserted.

## Independent review (Opus, manually-created isolated worktree)

This environment doesn't support the `Agent` tool's automatic worktree
isolation for a non-git top-level workspace, so the reviewer worked in a
`git worktree add --detach` checkout at a throwaway path instead — same
isolation guarantee (a disposable checkout, safe to revert/restore files
in), different mechanism to get there.

Verdict: **no blockers.** Two should-fix findings, six non-blocking notes.
Both should-fix items were fixed and independently re-verified in the same
session; both of Tester's own two flagged items were checked and resolved
(one confirmed pre-existing/accepted, one refuted as a misread).

**F1 (should-fix, fixed) — `TestIndexQuickPay_FollowsPreferredMethodSetting`
was a false-pass test.** Its two original assertions (`data-method="cash"`,
`⚡ Cash`) can't distinguish "the preferred-method reorder is wired
through" from "the hardcoded zero-state fallback rendered" — both produce
identical output when the preferred method happens to be cash. Reviewer
proved this by reverting only the `defaultPayMethod` production wiring:
the test still passed. **Fix:** added a `quickPayButtonSnippet` test helper
that isolates the quick-pay button's own tag (from `data-testid="quick-pay"`
to its `</button>`) — needed because a whole-page substring search has a
second false-positive source too: the overlay's own pay-grid Cash button
independently renders the identical escaped `hx-vals` string for
`method=cash`, so even the "assert the escaped jsonVals form" fix reviewer
first suggested would have false-passed against the whole page body. Scoped
all three tests in the file to this snippet, and added the escaped-vs-
literal `hx-vals` discriminator to the preferred-method test. Re-verified
personally: reverted the wiring → `TestIndexQuickPay_LabeledWithFirstActiveMethod`
and `TestIndexQuickPay_FollowsPreferredMethodSetting` both now fail with
real assertion errors quoting the fallback markup; restored → all 3 pass.

**F2 (should-fix, fixed) — the `@media(max-height:720px)` override cost
`.products` more than its own diff recorded.** Reviewer measured HEAD
against the pre-diff base on the identical harness: at 1024×600 the
override was the intended tradeoff (`.products` internal-scroll overflow
0px→113px, by design — that's what buying quick-pay's headroom costs).
But at 1024×**700** and 1024×**720** specifically, `.products` went from
**0px overflow (fits, no scroll) on `main`** to **50px/38px overflow**
under the 720px threshold — degrading an already-fine case purely because
the threshold sat above where quick-pay's own need actually stops (~710px,
per Dev's own measured break-even), not because quick-pay needed the extra
margin there. **Fix:** narrowed the threshold to 710px — exactly the
measured point where quick-pay's internal overflow already reaches 0 under
the *base* ratio — so 711–720px now reverts to the base ratio and keeps
`.products`' no-scroll case, while quick-pay itself stays clear (already
clear from ~680px per Dev's own numbers, well inside the new boundary).
No new single-pixel cliff introduced (reviewer's own concern, echoing the
2026-08-30 doc's B3 finding): re-ran the full tender-panel-reachable suite
(8/8 pass) plus the complete Go/guard/Playwright gate after the change —
all green, see "Gate" below.

**Tester's two flagged items, checked by the reviewer:**
- Held-sales strip needing an internal scroll to see at 1024×600 —
  **confirmed pre-existing and already formally accepted**, written up
  verbatim in `docs/code-reviews/2026-08-30-payment-trigger-clipping-1252.md`'s
  own "Explicitly deferred" section. Not a regression from this diff
  (`.tender`'s outer scroll/client height measured identical across every
  held-sale count).
- The Go test comment describing html/template's quote-escaping —
  **refuted, the comment is accurate.** html/template escapes template
  *actions* (`{{ jsonVals ... }}`), never static literal text — both
  branches are asserted and both pass. No change needed.

**Six non-blocking notes recorded, not requiring a fix this cycle:**
`guard-i18n.sh` passing isn't itself proof of "no new hardcoded string"
(the guard doesn't check plain-markup prose) — but the reviewer's own
direct read confirms the three new strings are genuinely exempt (decorative
⚡, live DB `.Name` data, a keyed `T` call), matching this file's existing
convention; the worst-case 1920×800@2×-scale tier wasn't extended to
quick-pay in the original test set (measured by the reviewer: clips but
stays scroll-reachable, a worsening of an already-accepted condition, not
a new failure mode — not fixed this cycle); the manual's "until a
preferred method is set up, it takes cash" line is imprecise (it actually
uses `payMethods[0]`, the first active method by sort order, which is only
cash on the shipped default seed) — filed as ut-docs#1377 rather than
edited under review pressure, since it's a documentation-precision issue,
not a functional defect, and deserves its own accuracy pass rather than a
rushed one-line edit in this diff; no fee hint on quick-pay unlike the
overlay's pay-grid buttons (defensible — the preferred method is by
definition the house/cheapest provider — but a real divergence worth
recording); an a11y nit that the accessible name doesn't say "charges";
and a commit-message inaccuracy about the pre-diff button count (3, not 2
— New Sale/Hold Sale/Payment).

## Verified beyond automated tests

- **Nil-safety, proven not just reasoned about.** `defaultPayMethod` is set
  only inside `if len(payMethods) > 0`; reviewer additionally reverted the
  entire Go-side computation (removing the map key outright, not just
  nil-ing it) and confirmed HTTP 200 + fallback render + no panic.
- **Offline-first parity, empirically checked.** `hx-include="#offline-flag"`
  reaches into the (possibly-closed) overlay's own hidden input; reviewer
  ticked the offline override, closed the overlay, tapped quick-pay, and
  confirmed the POST body carried `offline=1` — full parity with the
  pay-grid buttons.
- **Empty-basket safety.** An accidental tap on the always-visible charge
  button can't create a zero-value sale: `pos_api.go` returns 400 "no
  items in basket", surfaced via the existing global `htmx:responseError`
  alert — no silent no-op, no fiscal record.
- **Long-label graceful degradation.** A 64-char method name wraps the
  button (51px → 66.6px) without breaking the measured clearance —
  `.tender-scroll`'s `flex:1` absorbs it.
- **Manual + screenshots genuinely ship with the feature, not just claimed
  to.** Reviewer opened the regenerated PNGs directly: `sell.png` (en)
  shows the new full-width row; `sell.png` (fa) shows it correctly
  mirrored under `dir="rtl"`. All four locales' prose is real translation,
  not stale English carried over.
- **No secret-shaped value or real client/shop name anywhere in the diff**
  (grepped for both independently).
- Labeled (non-fallback) branch confirmed **twice**, independently: via a
  mutation-tested Go render test, and via Tester's live driven run with a
  real seeded `payment_methods` row and an actual completed sale through
  the browser.

**Screenshots taken, not just claimed:** 1024×600 with a held sale (both
by Dev and independently by Tester), 360px phone, RTL fa at 1280×800 — all
actually opened and read, not trusted from exit codes alone.

## Gate (re-run after both review fixes, this session)

`gofmt -l .` empty · `go build ./...` clean · `go vet ./...` clean ·
`go test ./...` — **all packages ok, 0 failures** (including the
mutation-tested `internal/pages/index_quickpay_test.go`) · all 19
`ci.yml` `build`-job guards pass, including a fresh `guard-docs-shots.sh`
run after `make docs-shots` regenerated screenshots for the F2 CSS change
(92/92 screenshots) · full Playwright suite (both `default` and `auth`
projects): **267 passed, 1 failed**.

The one failure — `shifts-tips-osk-1272.spec.ts`'s `setOskMode` navigation
to `/settings` (`net::ERR_ABORTED`) — is the **pre-existing, already-
tracked flake** ut-docs#1288 ("setOskMode navigation races to /shifts on a
fresh page"), unrelated to anything this diff touches (settings/shifts/OSK
navigation, not the sale screen or tender panel). Confirmed by re-running
that spec file alone immediately after: 2/2 passed. Not a regression from
this change.

`tender-panel-reachable.spec.ts` specifically: **8/8 passed**, including
all 4 new quick-pay tests, after the F2 CSS threshold change.

## Explicitly deferred / follow-up

- **ut-docs#1377** (filed this session) — the manual's "it takes cash"
  fallback line is imprecise; should describe the actual first-active-
  method behavior, not assume cash. Documentation-accuracy fix, not
  blocking.
- 1920×800 @ 2× UI-scale quick-pay clipping (scroll-reachable, matches the
  existing `payment-open` button's accepted behavior at the same tier) —
  noted, not filed as a new card since it doesn't regress anything already
  accepted.
- No fee hint on quick-pay — noted as an intentional/defensible gap, not
  filed.
- ut-docs#1288 (pre-existing e2e flake) — unaffected, no action needed
  here.

## Safe-to-merge verdict

Yes. Independent review's two should-fix findings are both fixed and
personally re-verified (F1 via a real revert→fail→restore→pass cycle, F2
via a full gate re-run including the complete Playwright suite). Both of
Tester's own flagged items resolved (one confirmed pre-existing/accepted,
one refuted). Full gate green except one pre-existing, already-tracked,
unrelated flake, independently confirmed not to be a regression.
