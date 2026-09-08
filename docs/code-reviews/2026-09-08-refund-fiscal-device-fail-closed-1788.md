# 2026-09-08 — Refund-path fail-closed check for missing fiscal-device evidence (ut-docs#1788)

## What shipped

`internal/pages/refund_page.go`'s refund handler gets the refund-side
mirror of ut-docs#1779's core fail-closed backstop for Turkey's
fiscal-device payment method (`fiscal.MethodKeyOKC`, manifest key
`"okc"`): if a plugin's blocking `payment.okc.refund` answer is
"approved" but carries no valid `fiscal_device` receipt (the *iade
fişi*), the refund is now refused (no return sale row created) instead
of completing with zero fiscal evidence.

Before this change, the refund handler called
`recordFiscalDeviceEvidence(..., pickDeviceEvidence(nil, refundResp))`
unconditionally after `pos.CompleteSale` — an OKC plugin approving a
refund with no receipt would still commit the return, then just quietly
skip recording evidence, exactly the gap #1779 closed on the sale side
and explicitly deferred here as out of scope for that card.

- The check runs immediately after the existing `blocked != nil` decline
  check (i.e. only once the plugin has genuinely approved), and before
  `pos.CompleteSale` — no return row exists on refusal.
- Gates on `fiscal.ParseDeviceEvidence(refundResp)` — the refund leg's
  OWN parsed response — never on the `pickDeviceEvidence` accumulator
  used later to persist evidence, mirroring #1779's post-review shape
  exactly (its first draft gated on the accumulator and had two live
  split-tender bypasses; a refund has exactly one payment leg, verified
  below, so that specific bypass shape doesn't apply here, but the
  reasoning is kept identical to its sale-side sibling rather than
  "accidentally correct by omission").
- New localized key `refund.error.fiscal_device_no_receipt`, distinct
  from the existing `refund.error.provider_declined` — same reasoning as
  #1779's `pos.toast.fiscal_device_no_receipt`: the device may already
  have taken the money before answering with no receipt, so "try another
  method" (the existing decline copy) risks a double refund. Same
  `http.StatusPaymentRequired` (402) as the sibling decline branch.
- New locale keys in all four shipped locales (`en`/`ar`/`fa`/`tr`).
- `web/help/{en,ar,fa,tr}/fiscal-device.md`'s existing "Good to know"
  bullet extended to cover the refund case too (previously sale-only);
  `web/help/img/manifest.json` regenerated via `make docs-shots` (prose
  hash only — no layout change, no new screenshot content).
- New tests in `internal/pages/refund_page_test.go`:
  `TestPostRefund_OKCPluginApprovesWithNoEvidence_RefundRefused` (the
  fail-closed case) and, added during review,
  `TestPostRefund_OKCPluginApprovesWithEvidence_RefundCompletes` (the
  happy-path pin — see review finding below).

## Independent review

Opus, fresh context, isolated worktree (`Agent(isolation: "worktree")`).
**Verdict: SAFE TO MERGE, no blockers.**

**Finding (MEDIUM, fixed):** the original diff had no positive-path
refund test — only the negative (no-evidence) case. The sale side has
`TestTenderHandler_OKCPluginReceivesBasketDetail` pinning the happy path;
the refund side had nothing equivalent, so a future tightening of the
check (e.g. also requiring a specific `receipt_kind`) could start
refusing every real Turkish refund with zero test failures. The reviewer
verified the happy path manually by mutating the plugin's answer to
carry valid evidence (200, as expected) but nothing pinned it. **Fixed**:
added `TestPostRefund_OKCPluginApprovesWithEvidence_RefundCompletes` —
same harness, an evidence-bearing plugin answer, asserts 200, one
`sales` row with `sale_type='return'`, and the receipt number actually
persisted to `fiscal_device_receipts`.

**Finding (LOW, deferred as follow-up, filed ut-docs#1794):**
`pickDeviceEvidence` has no `MethodID` check of its own — it parses a
`fiscal_device` object from *any* payment method's response, not just
OKC's. A non-OKC plugin whose response happens to carry a `fiscal_device`
object gets that evidence persisted and can flip
`signing_device_configured` true. Pre-existing and identical on the sale
side (`pos_api.go`'s own ut-docs#1779 comment already acknowledges this
explicitly) — out of scope for #1788, which is about *missing* evidence
on an approved OKC leg, not *fabricated* evidence from the wrong plugin.
Filed as its own card rather than widening this PR.

**Finding (LOW, deferred as follow-up, filed ut-docs#1795):** the
`method` form field is trimmed but not case-canonicalized, so
`method=OKC` (wrong case) fails the plugin-entry exact-match lookup,
never reaches the plugin, and then also fails the case-sensitive
`method == fiscal.MethodKeyOKC` gate — the refund silently completes as
an ordinary non-plugin refund instead of being routed to (or refused by)
the fiscal-device path. Lower severity than it sounds: the device is
never reached, no money moves through it, and the sale side has
identical case-sensitive matching (pre-existing, symmetric, not a
regression here). Filed as a data-hygiene/input-validation follow-up
rather than expanding this PR's scope.

**Findings accepted as-is (informational, no change needed):**
- Two log lines on refusal (`log.Printf` + `LogAndLocalizedError`'s own
  Info-level log) — deliberate: `LogAndLocalizedError` disappears
  entirely at `UT_LOG_LEVEL=warn/error`, and the stdlib `log.Printf`
  survives that filtering, which is the right call for a
  security-relevant fail-closed refusal.
- `fiscal.sign.start` fires before this refusal with no matching finish
  — pre-existing on both the sale and refund side (the existing
  `blocked != nil` decline path has the identical shape); not introduced
  by this diff.
- Help coverage lands on `fiscal-device.md` (route `/fiscal-device`),
  not the refund screen's own topic (`sell.md`, which has zero ÖKC
  coverage on the sale side either) — consistent with #1779's own
  precedent, not a regression here. A follow-up docs card covering both
  sides properly would be a separate, larger change.
- The refusal's copy points at "check the device" even when the OKC
  plugin has since been uninstalled/deactivated (old sale, now-gone
  plugin) — the operator's escape is picking `cash` in the same
  dropdown; refusing is still the correct fail-closed outcome, symmetric
  with #1779, just worth knowing for support.

## Verified beyond automated tests

- TDD genuinely red→green, independently re-verified by both Dev and the
  reviewer: reverted the fix (deleted the new check, keeping the test),
  confirmed the exact pre-fix failure (HTTP 200, one `sale_type='return'`
  row created with zero device evidence), restored, confirmed green.
- Mutation check: with the fix in place, changing the plugin's answer to
  carry valid evidence flips the result to 200 — proves the test tracks
  the plugin's actual answer, not just `method=="okc"`.
- Reviewer confirmed refunds have exactly one payment leg in this
  codebase (`refundPayments` always returns a one-element slice;
  `inventory_api.go`'s own `SaleType: "return"` producer hardcodes
  `cash`) — so there is no split-refund accumulator-laundering case
  analogous to #1779's two split-tender bypasses.
- Reviewer confirmed the real `tax-tr` reference plugin
  (`plugins/tax-tr/main.go`, `plugins/tax-tr/okc/bridge.go`) is
  unaffected — it already refuses a blank receipt on refund
  (`ErrNoReceipt`), so real Turkish refunds keep working.
- `go build ./...`, `go vet ./internal/pages/...`, `gofmt -l` (both
  changed files, clean), `golangci-lint run ./internal/pages/...` (0
  issues).
- Full `internal/pages` package test suite green (no regressions), full
  `go test ./...` (whole repo) green.
- CI-blocking guards run directly: `guard-data-access.sh`,
  `guard-page-http-error.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` — all green.
- All 4 shipped locale JSON files hold the same key count (2066), no
  drift; each new translation checked for being real, distinct,
  non-truncated prose (not leftover English or machine garbage).

## Safe-to-merge verdict

**Safe to merge.** No blockers found or introduced. One MEDIUM finding
(missing happy-path test) fixed in this same branch before merge; two
LOW findings are pre-existing, symmetric with already-shipped sale-side
behavior, and filed as separate follow-up cards (ut-docs#1794,
ut-docs#1795) rather than widening this PR's scope.

## Explicitly deferred

- ut-docs#1794 — `pickDeviceEvidence` has no `MethodID` check (a non-OKC
  plugin can fabricate `fiscal_device` evidence), on both sale and
  refund paths.
- ut-docs#1795 — payment-method key isn't case-canonicalized, letting a
  wrong-cased `method` silently sidestep the fiscal-device path (sale and
  refund alike).
