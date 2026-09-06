# Code review: payment overlay Hold Sale/New Sale reachability (ut-docs#1542)

- **Date**: 2026-09-06
- **Card**: universaltill/ut-docs#1542
- **Repo/branch**: `universal-till`, `fix/1542-payment-overlay-footer-reachability`
- **Complexity**: medium — Dev inline at Sonnet, review via an isolated-worktree
  Opus subagent

## What shipped

`#payment-overlay` (a fixed, right-anchored `min(26rem, 92vw)` panel,
`web/public/app.css`) does not reflow `.tender-default-footer` — measured
live (`getBoundingClientRect` center-point coverage), Hold Sale and New Sale
sat geometrically under the open overlay at every desktop viewport up to
~1440px wide, including this product's own 1024x600 kiosk floor. Not
hypothetical: `new-sale-closes-payment-overlay-1386.spec.ts`'s own "Hold Sale
closes an open payment overlay" test already had to force the viewport to
1920x1080 to get a real click to land at all.

- `web/ui/pages/index.html`: adds a second `<div class="tender-footer">`
  inside `#payment-overlay`'s own body (before the existing
  `.tender-footer.single` "New Customer" row) with two DUPLICATE buttons —
  `data-testid="payment-overlay-new-sale"` / `"payment-overlay-hold"` —
  byte-identical `hx-post`/`onclick`/`hx-on::after-request` wiring to the
  originals in `.tender-default-footer`. Same shape this file already uses
  for the phone-width New Sale duplicate (`kiosk-checkout-start-phone`,
  ut-docs#1345). The original buttons are untouched — this does not revert
  ut-docs#1252's decision to keep the primary Hold Sale button in the
  default view.
- `web/public/app.css`: bumps `#hold-modal`'s `z-index` from 500 to 550.
  Opening Hold Sale from *inside* the now-reachable overlay exposed a
  second, related latent bug: `#hold-modal` and `.payment-overlay` share
  z-index 500, and `.payment-overlay` is declared later in the HTML, so
  equal-z-index DOM-order stacking put the modal *behind* the still-open
  overlay — confirmed live by screenshot at 1024x600 (the modal's own
  submit button was entirely hidden). 550 clears the overlay while staying
  below `#osk`'s 1000, so the on-screen keyboard still renders above the
  modal's label field.
- New spec `e2e/tests/payment-overlay-footer-reachable-1542.spec.ts`: real
  hit-tests plus real completed actions (a hold, a reset) for both
  in-overlay copies at 1024x600, plus RTL (fa).
- i18n: no new keys — both duplicates reuse `hold.action` /
  `kiosk.checkout_start`, already translated in every locale.
- No ADR: a mechanical UI fix following an established in-repo pattern
  (duplicate-for-reachability) plus a stacking-order constant, not an
  architectural decision. Independently reviewed and agreed.
- Manual (`web/help/en/sell.md`): already describes Hold Sale/Payment
  adjacency at a flow level, not exact button positions — no prose change
  needed. `docs-shots` regenerated regardless (required — `web/ui/**`
  changed; `manifest.json`'s surface hash covers the whole tree even for
  an unrelated screen). Only `web/help/img/ar/sell.png` moved, from
  incidental demo-catalog/basket-state drift in the shared e2e DB between
  runs, not from this diff's own content.

## Independent review — findings and disposition

Reviewed by an Opus subagent in an isolated git worktree (`isolation:
"worktree"`, per this card's `complexity:medium` routing — Dev ran Sonnet,
review runs a stronger model), instructed to run everything itself and
independently re-verify the TDD claim via revert→confirm-real-failure→restore
rather than take it on faith.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| B1 | **Blocker** | `scripts/ci/guard-docs-shots.sh` failed on the reviewed diff (passed on `main`) — the fix changed `web/ui/pages/index.html`, so the manual's screenshot-freshness hash was stale. | **Fixed**: ran `make docs-shots`; guard now green (`✓ docs-shots guard: 25 routed topics × 4 locales screenshotted and fresh`). |
| N1 | Non-blocker | The RTL test's `hitTest()` calls had no preceding `toBeVisible()`, unlike the other two tests in the same file — a future regression that removed the copies would fail with an opaque 30s `locator.evaluate` timeout instead of a clear "not found". | **Fixed**: added explicit `await expect(...).toBeVisible()` before both hit-tests in the RTL case. |
| N2 | Non-blocker | The spec only asserted the *new* copies are reachable, never the underlying geometry problem itself — it would keep passing even if a future change made the duplicates redundant (e.g. the overlay got narrowed so the originals became reachable too), giving no signal to reconsider. | **Fixed**: added an assertion in each of the two main tests that the corresponding *original* button (`.tender-default-footer`) is still genuinely covered (`hitTest === false`) at 1024x600 — makes the spec self-justifying, per the reviewer's own manual probe confirming this is currently true. |
| N3 | Non-blocker | `phone-width-layout-413.spec.ts`'s `tender action grids collapse to one column at 360px` test used a bare `document.querySelector('.tender-footer')`, which now matches the NEW overlay-duplicate row first (DOM order) instead of `.tender-footer.single` — still passed (both collapse to 1 column via the same phone media query), but silently stopped measuring what its own name says, and the new row went unchecked by name. | **Fixed**: split into two explicit checks, `.tender-footer:not(.single)` (the new duplicate row) and `.tender-footer.single` (New Customer) — both asserted independently now. |
| N4 | Non-blocker | Button order inside the new overlay row was Hold Sale, New Sale — inverted from `.tender-default-footer`'s New Sale, Hold Sale, Payment order; easy to misread as a meaningful difference. | **Fixed**: reordered to match (New Sale, Hold Sale), with a one-line comment. |
| N5 | Non-blocker | New Customer (`.tender-footer.single`, unchanged, pre-existing) and the new in-overlay New Sale duplicate now sit in adjacent rows and both `POST /api/pos/reset`, but with different error handling: New Sale skips closing the overlay on an inline-error toast and refocuses the barcode field; New Customer always closes unconditionally and does neither. ut-docs#1252's own comment justified keeping New Customer separate *because* New Sale lived elsewhere in the layout — that reasoning is weaker now they're stacked. | **Not fixed — filed as a follow-up, universaltill/ut-docs#1624.** This is a product/UX call about aligning New Customer's error handling with New Sale's, not a defect this reachability card introduced (New Customer's own behavior is untouched), and touching it here would widen the diff beyond this card's scope. |
| N6 | Non-blocker | The two new in-overlay copies are visible *simultaneously* with the originals (unlike the phone-width New Sale duplicate, which is breakpoint-gated so only one copy is ever in the a11y tree at once) — `.payment-overlay` opens via `.show()` (non-modal), so nothing outside it is `inert`. A screen-reader/keyboard user traverses two identically-labelled "Hold Sale" controls with no distinguishing context, one of which may be visually covered. | **Not fixed — filed as a follow-up, universaltill/ut-docs#1625.** The underlying "originals are tab-reachable behind the overlay" condition is pre-existing (this card doesn't create it, only adds a second control with the same label); a proper fix (marking `.tender-default-footer` `inert` while the overlay is open) touches interaction beyond this card's default-view buttons and deserves its own UX pass rather than a rushed addition here. |

Also independently re-verified rather than taken on faith (by the review
subagent, and spot-checked by me after the N1-N4 fixes above):
- z-index 550 is safe: `app.css` is the only stylesheet in this repo, and
  nothing else sits between 500 and 1000; every other z-500 element
  (`#pfand-modal`, `#elevation-modal`, `#table-add-modal`) never coexists
  with `#hold-modal` (different pages); `#osk` (1000) and the toast (2000)
  still render above it; confirmed live with the OSK open from inside the
  modal from inside the overlay simultaneously.
- No other dialog opened from inside `#payment-overlay` has the same
  latent behind-the-overlay bug — everything else inside it is either an
  `hx-post` pay-grid button or the split-tender form (no dialog).
- The two duplicate buttons' `hx-*`/`onclick` attributes are byte-identical
  to their originals, attribute-by-attribute — no copy-paste drift.
- `.tender-footer`'s grid is direction-agnostic (`grid-template-columns:
  1fr 1fr`, `margin-top: auto` — no physical `left`/`right`), verified live
  under `fa`/RTL at 1024x600 (screenshot).
- Touch targets: both new buttons inherit base `.btn`'s 3rem/48px floor
  (no override on this row), same as New Customer beside them.
- Testid uniqueness: `payment-overlay-hold` / `payment-overlay-new-sale`
  are new and don't collide with any existing spec's text-based locators
  (those are all scoped to `.tender-default-footer button`).
- Author/committer identity on the reviewed commit: `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>` for both — a real,
  GitHub-linked human identity, not an AI-tool identity.

## TDD re-verification (revert → confirm real failure → restore)

Performed by the review subagent on the original fix, then re-confirmed by
me after applying the N1/N2 test changes above:

1. Reverted `web/ui/pages/index.html` and `web/public/app.css` to `main`
   (spec files kept). Re-ran `payment-overlay-footer-reachable-1542.spec.ts`
   → **3 failed**: `getByTestId('payment-overlay-hold')`/
   `'payment-overlay-new-sale'` — element(s) not found / visibility
   timeout, exactly the pre-fix state the card describes.
2. Restored the fix → **3 passed**.
3. Isolated the z-index half specifically: with only `app.css` reverted to
   `z-index: 500`, a direct hit-test on `#hold-modal button[type=submit]`
   opened from inside the overlay returned `hit:false`
   (`topElement:"btn pay-btn"`, i.e. the overlay's own pay-grid button sat
   on top); restored to 550 → `hit:true`. The bump is load-bearing, not
   speculative.
4. After adding the N2 self-justifying assertions, re-ran the full
   suite once more against the restored fix → still **3 passed**, and the
   new "original button still covered" assertions hold as expected
   (`false`, i.e. still geometrically covered) — confirming they measure a
   real, currently-true condition rather than a tautology.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...`, `golangci-lint run
  ./...` (0 issues) — all clean (this diff is HTML/CSS-only; Go tests are
  unaffected but `go test ./...` was run in full regardless — all packages
  green).
- `bash scripts/ci/guard-i18n.sh`, `guard-htmx-loaded.sh`,
  `guard-page-http-error.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` (after the `make
  docs-shots` fix), `guard-emoji-font.sh`, `guard-autofill-suppression.sh`,
  `guard-e2e-fixtures-import.sh` — all green.
- Full Playwright e2e suite (`npx playwright test --project=default`,
  `e2e/`), not just the new spec: **324 passed**, including every spec
  touching `payment-overlay`/`hold-modal`/`tender-footer`
  (`new-sale-closes-payment-overlay-1386`, `tender-panel-reachable`,
  `payment-overlay-osk-1385`, `phone-width-layout-413`, `rtl`, `sale`, …).
- **Visual-check attestation**: looked directly at screenshots (not just
  asserted on) at 1024x600 (overlay open, both new buttons cleanly laid
  out, no overlap/clipping), 1024x600 with the hold-modal open from inside
  the overlay (modal now renders on top, submit button visible and
  clickable), 1024x600 under `fa`/RTL (mirrors correctly, both new buttons
  correctly positioned/labelled), and 1920x1080 (both original and new
  duplicate buttons visible with no overlap, confirming no regression at
  the wide end). Did **not** separately screenshot a dark/theme-plugin
  variant — both new buttons reuse the base `.btn`/`.btn.secondary`
  classes verbatim, identically to the pre-existing "New Customer" button
  in the same footer, so theme interaction risk is judged equivalent to
  that already-shipped control, not a gap worth a special-cased check.
- Git identity checked before every commit in this cycle: real
  GitHub-linked address, never an AI-tool identity.

## Explicitly deferred (not silently dropped)

- N5 (New Customer vs. New Sale error-handling divergence, now that both
  sit in the overlay) → universaltill/ut-docs#1624, new Backlog card.
- N6 (no `inert` treatment of the default-view row while the overlay is
  open, so screen-reader/keyboard users see duplicate same-labelled
  controls) → universaltill/ut-docs#1625, new Backlog card.

## Verdict

**Safe to merge.** The one Blocker (stale `docs-shots` manifest) is fixed
and the guard now passes; all five non-blocker findings that were in scope
for this card are fixed and re-verified; the two that are genuine
follow-on product/UX decisions are filed as separate Backlog cards rather
than silently dropped or used to widen this diff. Full gate green, full
e2e suite green, TDD claim independently re-verified twice.
