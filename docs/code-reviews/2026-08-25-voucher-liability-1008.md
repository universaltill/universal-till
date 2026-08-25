# Code review: voucher liability tracking (ut-docs#1008)

## Change

Multi-purpose vouchers (§3 Abs. 13 UStG) as a 0% VAT liability class,
distinct from article revenue: `vouchers`/`voucher_transactions` tables
(migration 067), issuance wired into `pos.CompleteSale` as a sale-level
event (never a `sale_lines` row — a voucher is not an article), redemption
riding the existing `"voucher"` payment method with real per-voucher
balance tracking, and a new "GUTSCHEINE" day-close footer section
reporting issued/redeemed counts and amounts distinctly from
`Artikelumsatz`. Single-purpose vouchers (VAT at issue) and DATEV posting
of the liability were split to follow-up cards (ut-docs#1037, #1036) during
BA scoping — the former needs a business decision on point-of-sale UX, the
latter is blocked on DATEV export not existing yet (ut-docs#1005).

Builds on universal-till#524 (ut-docs#1003, per-VAT-rate day-close bands,
merged this same cycle) and universal-till#525 (ut-docs#1007, tips by
payment method) — this branch was rebased onto both via a merge commit
before review, resolving the expected `EODReport`/`pos_repo.go` struct
conflicts.

## Review process (this card, in order)

1. **First independent review — Opus, fresh context, no part in writing
   the feature.** Found **2 blocker-class and 3 major-class real bugs**,
   each confirmed with an executable probe written, run, and cleanly
   removed (verified `git status` empty afterward), plus 4 minor issues:
   - **Blocker:** a voucher issue folds its face value into `sales.total`
     but not `subtotal`/`tax_total`, breaking `pos.InferTaxInclusive`'s
     pricing-mode identity (`total == subtotal − discount + serviceCharge`).
     Reproduced: an inclusive-priced sale with a voucher is misread as
     exclusive, so `computeRefundTotal` adds VAT a second time on refund —
     an 11.90 item refunds at 14.16. Same misinference corrupts the
     day-close VAT bands and invoice VAT table for the same sale shape.
     The existing test for voucher exclusion from article figures asserted
     only `subtotal`/`tax_total`/`total`, never the inference — a genuine
     false-pass.
   - **Blocker:** voiding the sale that issued a voucher leaves the
     voucher `active` with full balance (no link between voucher tables
     and sale status), while the sale drops out of Gross/Net — free goods
     with no till record, still counted on the GUTSCHEINE footer since
     `VouchersIssuedRedeemedForRange` never joined `sales`.
   - **Major:** `issue_vouchers` validated only `amount <= 0`, no upper
     bound; two vouchers near 2⁶² plus 1 minor unit tendered wrapped
     `sales.total` negative, passing the payment-coverage check and
     minting vouchers with astronomic balances.
   - **Major:** over-tendering a tracked voucher (e.g. a 50.00 voucher
     against a 10.00 basket) silently confiscated the excess — change and
     tips are correctly forbidden on a voucher redemption, which is
     exactly what turns overtender into permanent loss instead of a
     refusal.
   - **Major:** a voucher could be issued and redeemed inside the *same*
     sale, fabricating an issue+redemption pair that inflates both
     GUTSCHEINE counters and Gross from nothing.
   - **Minor ×4:** redemption `VoucherID` skipped the issue path's
     length/control-char validation; the Z-report's own doc comment
     asserted an identity vouchers now break without saying so; a
     `RowsAffected` error was silently mislabeled as insufficient balance
     and the redemption race guard had no test that would fail if removed;
     `classifyTenderError` matched voucher errors by raw substring instead
     of `errors.Is` against the typed sentinels.

   Full findings on ut-docs#1008.

2. **Fix pass (Fable, fresh context, given the reviewer's exact findings +
   file:line references).** All nine findings fixed:
   - Persisted `voucher_issue_total` on the `sales` row itself (migration
     068, same precedent as `service_charge_amount`), threaded into
     `InferTaxInclusive`'s identity and every caller (`eod_tax_bands.go`,
     `refund_page.go`'s `saleIsTaxInclusive`), `data.SaleDetail`,
     `data.EODTaxBandSale`, and `sales_archive`/`reset_archive_repo.go`.
   - Added `POSRepo.VoidVouchersIssuedInSale` — a race-safe, guarded-UPDATE
     cascade (same pattern as `DebitVoucherForRedemption`) called inside
     the *same transaction* as the sale-status update: voids an untouched
     voucher, refuses the whole void (rolling back the status change too)
     if the voucher was already redeemed elsewhere. `VouchersIssuedRedeemedForRange`
     now excludes a voided sale's transactions via a permissive `LEFT JOIN`
     that still counts a reset-archived (missing) sale — preserving the
     original no-FK design's reset-survival property.
   - `MaxVoucherIssueAmount` ceiling (€1,000,000.00 minor units) enforced
     both at the API boundary and in `computeSaleTotals` (defense in
     depth).
   - A voucher redemption payment is now rejected if its amount exceeds
     the sale's outstanding total at that point in the payment list.
   - An up-front guard rejects a same-sale voucher-issue/voucher-redemption
     ID collision.
   - All four minors fixed: shared `validateVoucherID` on both paths,
     corrected doc comments, proper `RowsAffected` error handling plus a
     new 20-iteration concurrent-redemption test, `errors.Is` sentinel
     matching in `classifyTenderError`.

   Every fix has a regression test personally verified (by the fix-pass
   agent, and spot-checked again in review round 2) to fail without the
   fix and pass with it.

3. **Second review round (Opus, fresh context) — scoped to the fix pass,
   not a full re-review**, per this pipeline's process-depth rule (a
   second round is earned by a blocker-class finding, and stays scoped to
   the fix). Verified each of the 9 findings against the actual diff, not
   the fix-pass agent's self-report:
   - Confirmed `InferTaxInclusive` has exactly two production callers and
     both were updated (adding the parameter is compile-breaking, so a
     missed site could not build).
   - Confirmed `InsertSale`'s SQL genuinely writes the new column
     (placeholder count checked against the column list), and the archive/
     restore column lists stay symmetric.
   - Confirmed the refund regression test drives a real `POST /api/refund`
     and asserts the correct (not inflated) amount — not just that a
     boolean flips.
   - Confirmed the void cascade and status update share one DB transaction
     (a refusal rolls back both), and directly probed that a reset-archived
     (deleted) sale's voucher flows still count on the day-close report —
     only `status='voided'` is excluded, preserving the original design.
   - Probed the overtender fix in both payment-list orderings and proved
     (informally) no legitimate covering sale can be rejected regardless of
     order — the cap only ever bounds a voucher's maximum debit.
   - Probed the same-sale collision guard's whitespace-normalization
     ordering directly (issued `"  GS-WS  "` vs. redeemed
     `"\tGS-WS \n"`) — correctly rejected.
   - Verified the LAN-sync backward-compatibility claim for the new
     `voucher_issue_total` JSON field by reading the actual decoder (no
     `DisallowUnknownFields`) rather than trusting the claim.
   - Re-ran the full gate independently: `gofmt`, `go build`, `go vet`,
     `go test ./internal/data/... ./internal/pos/... ./internal/pages/...
     ./internal/db/...`, `guard-data-access`, `guard-i18n`,
     `guard-docs-shots`, `guard-help-topics`, `guard-compliance-claims` —
     all green.
   - **Two residual notes, both filed as follow-ups, neither a blocker:**
     ut-docs#1052 (the overflow guard's safety margin is arithmetic-incidental
     rather than structurally capped — wrapping still needs a multi-terabyte
     request body) and ut-docs#1053 (voucher issue/redemption doesn't
     replicate over LAN sync — pre-existing in the original feature, not
     introduced by the fix pass; the F1 fix strictly improves this path's
     tax-inference correctness on replay without closing the gap).

4. **This pass (Sonnet, orchestrating session).** Independently re-ran the
   full gate a third time on the final committed state (not trusting
   either subagent's self-report): `gofmt -l .` clean, `go build ./...`
   clean, `go vet ./...` clean,
   `go test ./internal/data/... ./internal/pos/... ./internal/pages/...
   ./internal/db/...` all `ok`, full `go test ./...` (repo-wide) clean,
   and all 16 CI-blocking guards from `.github/workflows/ci.yml`'s build
   job pass, including `guard-docs-shots` (screenshots regenerated,
   92/92 passed) and `guard-i18n` (1244 keys, all locales match).

## Known, deliberately out-of-scope follow-ups

- ut-docs#1036 — DATEV posting of the voucher liability to account 1796
  (SKR03), blocked on ut-docs#1005 (DATEV export itself doesn't exist yet).
- ut-docs#1037 — single-purpose vouchers (VAT at issue), a separate
  point-of-sale UX decision from this card's multi-purpose-only scope.
- ut-docs#1052 — harden the overflow guard with a structural count/
  running-total cap, not just the per-voucher ceiling (review round 2).
- ut-docs#1053 — voucher issue/redemption doesn't replicate over LAN sync
  (pre-existing gap, review round 2).
- No cashier UI for issuing a voucher — API-only entry point
  (`issue_vouchers` on `/api/pos/tender`) is this card's accepted minimal
  shape; a UI pass is future work.

## Outcome

Merging as-is. Closes universaltill/ut-docs#1008.
