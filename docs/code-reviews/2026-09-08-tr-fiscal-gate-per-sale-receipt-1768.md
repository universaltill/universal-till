# 2026-09-08 — TR fiscal gate: per-sale device receipt, not a one-time posture flag (ut-docs#1768)

## What shipped

Turkey's fiscal hard gate (`internal/fiscal`, ADR-0048/ADR-0083, extended
to Turkey by ut-docs#1208) proves a shop's YN ÖKC device has *ever*
printed a receipt (`fiscal.signing_device_configured.tr`), not that any
given sale actually went through it. A system-of-record TR shop whose
device was confirmed once could complete every subsequent sale — cash
included — with zero fiscal-device involvement, exactly the gap
ut-docs#1750's review recorded: "cash included — a cashier tapping Nakit
completes a sale with no mali fiş, no marker and no receipt notice."

Law No. 3100 requires a mali fiş **per sale**, and a competitor-research
pass (SumUp/ready2order-class documentation was unreachable; Turkish
tax-advisory sources instead) confirmed businesses must ensure the
receipt/invoice issues **at the moment each sale occurs**, with a
per-occurrence penalty (10,000 TL per un-issued fiş) — a per-sale
obligation, not a one-time device-pairing event. No legally-sanctioned
"declare unsigned, reconcile later" carve-out (Germany's KassenSichV
TSE-outage allowance) was found for Turkey, so the design leans on the
existing owner-override machinery rather than inventing a new one.

- `internal/fiscal/fiscal.go`: new `RequiresPerSaleDeviceReceipt(country)`
  — `true` only for `"TR"`, deliberately excluding `"DE"` (Germany's TSE
  is already per-sale via `fiscal.sign.ask`/`declareUnsignedFiscalSale`,
  ADR-0044). The one-line extension point for the next per-sale-receipt
  market, mirroring `RequiresHardGate`'s own shape.
- `internal/pages/pos_api.go`'s `completeTender`: a new check, placed
  right after `enforceFiscalGate` and **before** the
  `payment.<key>.authorize` loop — a shop in a
  `RequiresPerSaleDeviceReceipt` market, system-of-record, with
  `gate.Decision == fiscal.Allowed` (device confirmed and healthy) must
  declare at least one payment leg keyed `fiscal.MethodKeyOKC`, or the
  sale is refused (new `fiscalDeviceRequiredError`) with no plugin round
  trip and no sale row. Scoped to `Allowed` only:
  `BlockedNeverConfigured`/`BlockedTSEFailing` already refused the sale
  earlier, and `AllowedWithOverride` already gets its own
  `unsigned_override` audit marker (unconditioned on country) —
  independently verified that marker still fires with no OKC leg present,
  which satisfies the card's "recorded in a way that is honestly
  distinguishable" acceptance criterion for that posture without new code.
- Both tender surfaces handle the new error with the *existing* #1779
  copy (`pos.toast.fiscal_device_no_receipt` /
  `selforder.checkout.fiscal_device_no_receipt`) — deliberately no new
  i18n key: to the operator it's the same instruction either way ("route
  the sale through the fiscal device"), and reuse avoids a 4-locale key
  addition for a message that already exists.
- `web/help/{en,tr,fa,ar}/fiscal-device.md`: one new "Good to know" bullet
  naming this refusal explicitly. `make docs-shots` re-run (the guard's
  surface hash covers `pos_api.go`/`self_order_shop.go` as whole files, so
  the code change alone required a refresh regardless of the doc edit).
- Tests in `internal/pages/pos_api_test.go`: cash-only refusal, the
  positive single-OKC-leg case, DE non-regression, shadow-mode
  non-regression, an active-override cash-only case (allowed + audited),
  and two adversarial cases from the independent review (below).

## Independent review (Opus, fresh context) — two blockers found and fixed

The first draft gated on the sale-wide `deviceEvidence` accumulator
(`pickDeviceEvidence`'s result) rather than payment-leg identity, and ran
the check *after* the `payment.<key>.authorize` loop. The reviewer broke
both, empirically:

1. **Laundering (correctness).** `pickDeviceEvidence` takes evidence from
   *any* method's authorize response with no `MethodID` check of its own
   — the exact shape ut-docs#1779 was fixed away from. A non-OKC plugin
   (card/QR/demo) returning a forged `fiscal_device` object satisfied the
   evidence-based check with **zero** OKC legs: confirmed HTTP 200, one
   sale row. Fixed by gating on payment-leg identity
   (`p.MethodID == fiscal.MethodKeyOKC` over the declared `payments`)
   instead of the evidence accumulator — ut-docs#1779's own per-leg check
   still guarantees any OKC leg that *is* present carries valid evidence,
   so the two checks compose correctly. Regression:
   `TestTenderHandler_TRSystemOfRecordForgedEvidenceFromNonOKCPlugin_StillRefused`.
2. **Money captured before refusal (correctness).** Because the original
   check ran after the authorize loop, a split cash+card tender with no
   OKC leg would let the card plugin capture the customer's money before
   the sale was refused with no unwind path. Presence of an OKC leg is
   fully knowable from the declared `payments` alone, so the check moved
   to run *before* any `payment.<key>.authorize` call. Regression:
   `TestTenderHandler_TRSystemOfRecordCardOnly_RefusedBeforeAuthorize`
   (asserts the non-OKC plugin's authorize hook is never invoked at all).

Also flagged and fixed: no DE regression test existed (added
`TestTenderHandler_DESystemOfRecordCashOnly_NotBlocked` — proves
`RequiresPerSaleDeviceReceipt` scoping can't silently widen to `DE`), and
the original refusal-status assertion couldn't distinguish this refusal
from an unrelated decline (tightened to assert the specific copy).

Verified, no finding: the `AllowedWithOverride` exclusion is correct (the
existing `unsigned_override` marker fires regardless of country and
regardless of OKC-leg presence); the i18n-key reuse is fine (`guard-i18n.sh`
green, both keys present in all four locales); the refund path
(`refund_page.go`) never calls `completeTender` and has neither a #1779
nor a #1768 check — correctly deferred to the already-filed ut-docs#1788,
which the reviewer additionally noted also needs the same `pickDeviceEvidence`
care.

## A self-caught test-quality issue, found after the independent review

`web/help/*/fiscal-device.md` already documents (all four locales, pre-dating
this card) that the device must take the *whole* sale amount — a split
between the device and another method is refused **by the plugin**
(`plugins/tax-tr/okc/protocol.go`'s `ErrSplitTender`). An initial version of
this PR's test suite included a "split cash+OKC allowed" positive case using
a stub plugin that skipped that amount-matching validation entirely — a
scenario the real `tax-tr` plugin would never let succeed. Removed before
review sign-off; the single-leg positive test
(`TestTenderHandler_TRSystemOfRecordWithOKCEvidence_Allowed`) is the
realistic positive case, and a comment on the new check itself now says
explicitly that leg-amount matching is plugin-owned, out of this card's
scope.

## Verification

- Full Go suite green (`go test ./...`, 0 failing packages);
  `gofmt -l .` clean; `go vet ./...` clean.
- TDD verified personally: reverting the non-test diff (keeping the new
  tests) reproduces the pre-fix bug —
  `TestTenderHandler_TRSystemOfRecordCashOnly_NoDeviceEvidence_Refused`
  fails with HTTP 200 (sale silently completes) against the original code.
- Guards run locally: `guard-data-access`, `guard-page-http-error`,
  `guard-compliance-claims`, `guard-i18n`, `guard-kiosk-engine`,
  `guard-help-topics`, `guard-docs-shots` (after `make docs-shots`,
  104/104 screenshots passed) — all green.
- Not run / pre-existing sandbox limitations, unrelated to this diff:
  `guard-commit-attribution.sh` (expects a piped `git log` commit range,
  not a bare local invocation) and `guard-deadcode-baseline.sh` (desktop
  cgo build needs `gtk+-3.0`/`webkit2gtk-4.1`, not installed in this cloud
  sandbox).

## Explicitly out of scope

- The refund path's identical gap is ut-docs#1788 (already filed, Ready).
- Leg-amount validation (OKC must cover the whole sale) — plugin-owned,
  pre-existing, unaffected by this change.
- Hardware verification against a real YN ÖKC — out of reach in this
  sandbox, same caveat as every other tax-tr card.
