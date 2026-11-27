# Review — refund: cashier picks which card payment gets the refund (ut-docs#6648)

Date: 2026-11-26. Branch `fix/6648-refund-pick-card-leg-v3`. Built by Sonnet 5.5 (Dev),
reviewed by Opus 5.5 (independent subagent, isolated worktree). Lane `lane:cloud-41`.

## What shipped
- `internal/pages/refund_page.go`: one helper `refundLegsFor` (traced payments of a
  method, netted per leg by #6649's `netRefundLegCaps`) used by both the refund
  screen and `POST /api/refund`; `allocateRefundToLeg`. New form field
  `refund_leg` (payment id or `all`):
  - a chosen payment gets the whole refund; if it has less left → 409
    `refund.error.leg_exhausted`, never spilled onto another payment;
  - an id that is not a traced payment of this sale and method → 400
    `refund.error.leg_unknown`;
  - absent or `all` keeps the payment-order split (existing callers/tests unchanged);
    ignored when the method has fewer than 2 traced payments;
  - both refusals, and the existing `method_exhausted` 409, now fire before
    `fiscal.sign.start`, so a refused refund opens no fiscal transaction.
  - GET no longer lists a method twice in "Refund to" for a two-card sale.
- `web/ui/pages/refund.html` + `web/public/app.css`: one `<fieldset>` per method
  with ≥2 payments (amount, time, "… left to refund", fully-refunded rows disabled,
  "All payments, in the order they were paid" last), required radio, no
  preselection; shown only for the selected method; 48px rows, logical CSS,
  `<bdi>` for RTL; required-pick bubble text via page-local `var T`.
- 5 `en.json` keys + `en.meta.json` metadata (English only, ADR-0160).
- Help: `web/help/en/sell.md` step 6, `web/help/en/payments.md`.
- Tests: `internal/pages/refund_leg_choice_test.go` (10 tests on `newRefCardRig`);
  e2e `e2e/tests/refund-pick-card-leg-6648.spec.ts` with seed
  `e2e/seed_split_card_sale` (two traced card payments, no plugin).
- ut-docs `reference/payment-provider-contract.md`: `original_payments` may name
  only the chosen payment (branch `docs/6648-refund-leg-choice`).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | important (pre-existing) | After `outcome_unknown`, a retry with a different payment/quantity/method gets a new journal key and can refund twice; nothing checks `PendingRefundSales` first. The picker makes it one tap easier. | deferred → ut-docs#7104 |
| T1 | minor | 409 message partly under the fixed status bar at 1024×600 kiosk and 360 | fixed (`scroll-margin-block` on `#refund-msg`) |
| T2 | minor | ✗ message stays after switching method to cash | fixed (cleared on method change) |
| T3 | minor | at 360 the "All payments" radio sat on its own line | fixed (flex basis on the label text) |
| U6 | note | two payments with identical amount and time and no masked PAN look the same | accepted (rare; time shown to the minute) |

Checked OK by the reviewer: chosen leg never spills; tap binding / `TransactionRef`
use the chosen payment; disabled hidden fieldset is not submitted or validated by
htmx/browser; concurrency covered by the per-sale lock (#6742) with caps re-read
under it; GET and POST share `refundLegsFor`; e2e seed paths from `UT_DATA_DIR`.

## Verification
- TDD re-verified by the reviewer: `refund_page.go` reverted to `04fe7444b` (plus a
  stub of the new helper so it compiles) → ChosenLeg, TooShort, Netted, UnknownLeg,
  page and RefusalsOpenNoFiscalStart tests red for the right reasons; restored → green.
- Tester: 9 mutations, all red (two tests strengthened to catch "refusal before
  fiscal start" and `all` ordering); `go test ./...` 127 packages ok (pages and
  plugins rerun with `-timeout 30m` on an overloaded box); `-race` on the refund
  tests ok; `golangci-lint` 0 issues; 13 guards pass; the new e2e spec passed for real.
- Driven run of the real binary (windowed and `UT_KIOSK=1`) with the seed; screenshots
  at 1024×600 windowed/kiosk, 360×740 and fa (RTL) 360 for none picked, submit
  blocked, picked, fully refunded leg, 409 visible, switched to cash. Touch emulated.
  Status bar visible everywhere; lock/exit not rendered on an auth-off non-appliance
  till; German not checked (no ut-translations checkout). Post-review CSS fixes:
  tests + i18n/i18n-meta/rtl guards re-run, not re-screenshotted.

Pre-flight: help topic updated (sell.md, payments.md); docs-shots deferred → ut-docs#7105
(no refund screenshot existed); reference page updated (ut-docs payment-provider-contract.md);
README n/a (no stale text); en.json keys → en.meta.json updated; review record this file;
follow-ups filed (#7104, #7105). CI `guard-docs-shots`: `make docs-shots` re-run; only
`manifest.json` committed (sell topic hash + the two surface files). No existing
shot shows the refund page and step 6 is not in a `sell` shot; the PNGs this
container re-rendered differed only by environment noise, so they were not committed.

Verdict: ship after fixes — fixes applied.
