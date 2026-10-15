# Review — gift voucher top-up as a sale (ut-docs#4252)

**Date:** 2026-10-15 · **Lane:** `lane:cloud-24` · **Branch:** `feat/4252-voucher-top-up-sale`
**Built by:** Opus 5.5 (Dev subagent) · **Reviewed by:** Fable (independent, different model)

## What shipped
- **Top up** on the voucher detail page (`/vouchers/{id}`), gated on `settings`. It takes an amount and **Add to sale**. `POST /vouchers/{id}/top-up` validates the request, then adds a top-up line (an existing number with the added value) to the **cashier** basket (`d.Engine`) and redirects to the sale screen. Errors are inline and non-modal.
- At tender the line becomes `VoucherIssueInput{TopUp: true}`, a 0% multi-purpose liability inside `voucher_issue_total`. `CompleteSale` credits the existing voucher in the sale's own transaction (`data.TopUpVoucher`, a guarded UPDATE; a used-up voucher becomes active again; `original_amount` is unchanged) and writes one `top_up` ledger row. It never creates a voucher.
- **Migration 080** rebuilds `voucher_transactions` using the leaf-table recipe from 074, widening the CHECK to admit `top_up`.
- Day-end: `top_up` counts under **Issued** in both window functions, so the Z identity *bands = Net − vouchers issued* still holds.
- **Void of a top-up sale:** the credit is reversed by a guarded debit. It is refused once the value has been spent; a voucher that drops back to 0 goes back to *used up*. The reversal writes an `adjustment` row with the deterministic id `topup-void:<top_up row id>` and the voided sale's id. That row makes a repeat void a no-op, keeps the history summing to the balance, and drops out of the day-end with the voided sale.
- Receipts:
  - The on-screen receipt, printed receipt and reprint show "Gift voucher top-up", the number, +amount and the new balance. The new balance is taken from the ledger at the top-up row, so a reprint matches the first print.
  - The basket line is tagged "Top-up". The journal and history show Top-up and Top-up voided rows.
- An additional till does not offer top-up (a non-modal note, and the POST is refused). Follow-up card: ut-docs#4416.
- Help topic `vouchers` (en/de/tr/fa/ar), 17 new keys in every core locale, README.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | major | `/api/pos/tender` accepted `top_up` on body-supplied `issue_vouchers`. That bypassed the `settings` gate and the replica refusal: any session could credit a voucher, and a replica could credit its local mirror and journal the credit to the main till. | **Fixed.** A body `top_up` is refused with 400 before the basket's own top-up lines are folded in. `TestTender_TopUpFromBodyRefused` failed before the fix. |
| m1 | minor | A held or resumed top-up line whose voucher was voided, or pushed over the ceiling, failed at tender with redemption wording ("can't be redeemed", "use Redeem"), and the over-ceiling case had no wording at all. | **Fixed.** Every `TopUpVoucher` refusal also wraps the new `ErrVoucherTopUpRefused`. `classifyTenderError` matches it first and shows `pos.toast.voucher_top_up_failed` ("remove the top-up line"). `TestClassifyTenderError_TopUpRefused` and `TestVoucherTopUp_VoidedBeforeTenderToast` both failed before the fix. |
| m2 | minor | Voiding the *issuing* sale after a top-up is refused with `ErrVoucherRedeemedCannotVoid`. | **Accepted, pinned.** Refusing is the correct behaviour, because the customer has paid for the added value. `TestVoidIssuingSale_RefusedAfterTopUp` pins it. |
| m3 | minor | Migration numbering gap (078 → 080). | **Resolved.** universal-till#1961 merged `079_till_tls_pinned.sql` first, so this change takes **080**, the next free number. |
| m4 | minor | No currency check on top-up. | **Accepted.** `AdjustVoucherBalance` (#4253) has the same gap. The till has one active currency. |
| n1–n4 | nit | Unreachable not-found wrap; heading reuse for a top-up-only receipt; unstyled classes (now styled by the Tester fix); pack PRs. | Accepted, except the pack PRs, which are done in the same cycle (de/es/pt). |

**Tester bug:** on the on-screen receipt, the top-up label was squeezed into a one-word column over the number. #4224's `li > :first-child` flex rule no longer hit the number once the label became the first child. **Fixed** by targeting `.receipt-voucher-num` and giving the label and balance full width. `e2e/tests/voucher-top-up-4252.spec.ts` failed before the fix with "top-up label sits above the number".

## TDD re-verified by the reviewer
The reviewer reverted each fix and saw its test fail, then restored it:
- `reverseTopUpsInSale`: `TestVoidVouchersIssuedInSale_ReversesTopUp` failed with "after void … balance 3500, want 1000".
- The `CompleteSale` top-up branch: `TestCompleteSale_TopUpCreditsExistingVoucher` failed with "voucher id already exists".
- The day-end `top_up` arms: `TestVouchersIssuedRedeemed_TopUpCountsAsIssued` failed with "issued 1/1000, want 2/3500".

## Verified beyond automated tests (Tester, driven run)
The run used the real binary on a fresh data dir with Chromium:
- **Top-up and payment:** sell, then top up £10.50 → basket line "Top-up" with £0.00 tax → pay cash.
- **Receipt and records:** the receipt shows the label, number, +£10.50 and "New balance £30.50". The history has a Top-up row linked to the receipt, and the journal shows the entry.
- **Day-end:** EOD issued is 4 / £70.50 with gross reconciled.
- **Void:** voiding the top-up sale takes the balance from 30.50 back to 20.00, and the history shows "Top-up voided". A repeat void gets 409 with no double debit. A void after the top-up was spent gets 409.
- **Refusals:** zero or over-ceiling amounts give an inline 400. A voucher already in the sale gets 409. Single-purpose and void vouchers show the not-possible message and get 409.
- **Used-up voucher:** topping it up makes it active again.
- **Cashier**, on an auth-on till with a PIN login: 403 on the page and on the POST.

**Looked at:** the detail page at 1024×600 and 360px, light and dark, en, de and fa (RTL), with no overflow. The basket in en light, de dark and fa. The receipt in en, de dark and fa at 1024 and 360.
**Not looked at:**
- the printed ESC/POS receipt (no printer; a handler test covers it);
- a live replica pair (a handler test covers it);
- ar and tr;
- the phone basket sheet;
- touch hardware.

An out-of-scope finding (existing behaviour, not this change) goes to its own card: the on-screen receipt card in the dark theme has very low-contrast grey text.

## Gate
- `gofmt` clean; `go build ./...` and `go vet ./internal/...` OK; `golangci-lint` 0 issues.
- `go test` passes for `internal/{data,pos,db,print,pages}`. Dev also ran the full `./internal/...`.
- The ci.yml build-job guards are green, including i18n, data-access, help-topics, help-drift, kiosk-engine, no-showmodal, migration-version-collision, card-data-schema, core-neutral and docs-shots (surface hash refreshed; no rendered pixel of an existing screenshot changed). `guard-shellcheck-version` couldn't run here because there is no shellcheck binary.
- The e2e spec passes 6/6.

## Verdict
Safe to merge once CI is green. The de/es/pt language-pack PRs land in the same cycle.
