# Review record: tender decline notice in the payment overlay (ut-docs#6962)

## Change
- Bug: on a phone-width till (<= 900px) `#payment-overlay` covers the whole
  viewport, so a declined card tender's `#pos-alert` banner was hidden, and the
  402 body was replaced by the generic server-error text. The cashier saw
  nothing.
- `internal/pages/pos_api.go`: `paymentDeclinedError.MessageKey`, set by
  `tenderDeclineKey(exitErr)` from four provider-neutral plugin `decline_code`
  values (`no_reader`, `reader_offline`, `reader_busy`, `auth_failed`); unknown
  or absent code gives `pos.toast.payment_declined`. The 402 answer uses
  `cmp.Or(MessageKey, generic)`. The code is compared, never echoed.
- `web/ui/pages/index.html`: `#payment-notice` (`role="alert"`, `hidden`,
  `data-reusable`, `data-msg-server`) inside the overlay under the tabs.
- `web/public/app.js`: tender failures sent from inside the open overlay answer
  in `#payment-notice` instead of `#pos-alert`: 402 `text/plain` body (<= 300
  chars) via `textContent`, else `pos.error.server`; `sendError` uses
  `#pos-alert`'s network text; a 200 basket swap carrying an error toast is
  mirrored. Cleared on the next overlay request and on overlay `close`
  (captured, since `close` does not bubble).
- i18n: four `pos.toast.payment_declined_*` keys in `en.json` + `en.meta.json`
  (English only, ADR-0160). CSS: one logical-property rule.
- Docs: `web/help/en/payments.md` section "When a card payment is declined";
  `docs/plugin_guidelines.md` decline_code table.
- Tests: `internal/pages/tender_decline_code_test.go` (table + real plugin
  rig through `/api/pos/tender`: 402 body, no sale, no tender lock, basket
  kept); `e2e/tests/payment-decline-notice-phone-6962.spec.ts` (stubbed tender).

## Independent review
Author: Sonnet. Reviewer: Opus (different model). Scope: `git diff` + the two
untracked test files; design notes in notes-6962 (ba/architect/ux/dev).

Checked:
- XSS: the body only reaches the DOM through `textContent`; the server body is
  our own `httpx.T` string (`http.Error`, `text/plain`, nosniff).
- No second message: the overlay branch `return`s before `showAlert('server')`
  / `showAlert('network')`; requests from outside the overlay (basket
  quick-pay, other routes) and non-tender paths take the old path unchanged.
  `htmx:timeout` is unreachable for tender (`/api/pos/tender` is in
  `UT_UNBOUNDED_PATHS`). Prompt (elevation) elements keep the `outcome` path.
- Tender lock/basket: server change only picks a message key; decline stays a
  definitive `paymentDeclinedError` (no lock, no sale, basket kept, pinned by
  the rig test). Tap legs keep their own 200 tap-panel path.
- Dismiss: the shared `.notice-dismiss` handler only hides a
  `data-reusable` notice, so the next decline shows it again (e2e covers).
- Overlay close clears text and hides; a 200 error toast keeps the overlay
  open (`no-error-toast` guard), so the mirror is visible.
- Rules: no `showModal`/`confirm`, logical CSS, core-neutral (codes are
  generic, not vendor/country), help ships in the branch, offline-first (no
  network dependency added; network failure answers in the overlay).

## Findings
1. **Medium, fixed.** A tender refused with `HX-Trigger: card-reader-needed`
   (JSON body, `card_reader_tender.go`) from the overlay would have shown the
   generic "server error" in `#payment-notice`; `card-reader-picker.js` only
   withdraws that text from `#pos-alert`, so the stale generic message stayed
   next to the reader picker. Fix: the overlay branch skips responses carrying
   `card-reader-needed`, keeping the existing picker flow.
2. **Low, fixed.** The `htmx:afterSwap` mirror copied any `#basket` error toast
   while the overlay was open (e.g. a scan error), not just tender's. Fix:
   restricted to the tender path. (htmx 1.9.12's afterSwap detail carries no
   triggering element, so the path is the discriminator.)
3. **Low, accepted.** On a wide till where the overlay does not cover the
   basket, a 200-with-error-toast tender shows the same text in the basket
   toast and in the notice. Same message, not a second message; harmless.
4. **Info, accepted.** New `en.meta.json` entries sit among `refund.*` lines;
   the meta guard does not require ordering.

TDD re-verified inline: with `MessageKey: ""` `TestTender_DeclineCodeMapsTo402Message`
fails for the reader codes (body is the generic decline); restored, passes.

Pre-flight: `gofmt -l .` (clean), `go build ./...`, `go test ./internal/pages/
-run 'Tender|Decline|Payment' -count=1` (ok), `node --check web/public/app.js`,
guards `guard-i18n`, `guard-i18n-meta`, `guard-rtl-logical-css`,
`guard-no-showmodal`, `guard-no-native-confirm`, `guard-core-neutral`,
`guard-help-topics`, `guard-compliance-claims` (all pass). Not run: full
`go test ./...`, golangci-lint, the e2e spec (no browser run in review).

## Verdict
Approve with findings 1-2 fixed in the working tree. Run the e2e spec and the
full gate before merge.
