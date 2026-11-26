# ut-docs#7064 slice 1 — journal: which sale was refunded + Refunded / Partly refunded / Refund pending

Branch `feat/7064-journal-refund-status`. Built by Sonnet (Dev + fix round); independent review by Opus 5.5
(fresh context); Tester a separate Sonnet agent. BA / Architect / UX notes are summarised on the card.

## What shipped
- `/ui/journal`, the sale-screen mini journal and `/journal/{receipt}`: a return row shows a negative total and a
  "Refund of #X" link to its original sale (no status chip on the return row itself, UX).
- The original sale carries a text + icon chip: **Refunded** (completed linked returns ≥ total), **Partly refunded**
  with "£x of £y refunded", or **Refund pending** (a refund leg whose newest payment-leg-journal state is
  `unknown`/`warned`). Pending always wins and is never shown as Refunded; its amount line is hidden.
- Detail page: translated Sale/Refund tag, the chip, the amount line, and a sentence for pending (do not start a new
  refund; tap Refund again on the refund screen; after a restart ask a manager to check Payments needing attention).
  The detail page keeps a return's totals positive, like the printed receipt.
- New repo code `internal/data/journal_refund_repo.go` (`JournalRefunds`, `deriveRefundStatus`) and
  `PaymentLegJournalRepo.PendingRefundSales`: 3 queries per page, IN-lists chunked at 500, no N+1. A failed lookup is
  logged and only drops chips/links (page still 200, tender response still carries the OOB mini journal).
- `ListSalesJournal` gains only `s.id`, `s.sale_type` (both from migration 001: recovery safe mode unaffected).
- 7 English keys (+ meta), one `.journal-refund` CSS block (logical properties, 44px link on coarse pointer / kiosk),
  help paragraph in `web/help/en/reports.md`. No migration, no ADR (read-only view over existing tables).

## Findings (review round 1)
- S1 should-fix — pending advice sent people to Payments needing attention, which lists only `warned`/
  `reverse_failed`; an `unknown` leg is not there until a restart, and the refund screen says "tap Refund again".
  **Fixed**: wording covers both cases and keeps "do not start a new refund" (a new request id would refund twice).
- S2 should-fix — a return's detail page showed Total negative under positive Subtotal/Tax. **Fixed**: detail stays
  positive (receipt view), the Refund tag marks it; list stays negative. Help updated.
- Tester D1 — pending row also showed "£18.00 of £18.00 refunded". **Fixed**: amount hidden while pending (tests).
- N1 test seeded `void` instead of `voided`. **Fixed**.
- N2 range-bounds test could not fail. **Removed**, replaced by `TestPendingRefundSales_NewestRowPerLegDecides`.
- N4 `PendingRefundSales` scanned every refund leg row. **Fixed**: filters `state IN ('unknown','warned')` first,
  newest-row-per-leg kept via `NOT EXISTS` (older `unknown` + newer `captured` is not pending — tested).
- N3 accepted: `ListSalesJournal` has no status filter, so a voided return row would still show "Refund of #…";
  returns are not voidable in today's flows. Tracked with the follow-ups (#7069).
- Tester cosmetic: the half-disc icon for Partly refunded renders small; the word carries the meaning. Accepted.

## Verification
- Tester: `go test ./internal/pages/ -timeout 50m` ok (615 s, no -race; CI runs race); `internal/data/...`,
  `internal/ui`, `internal/recovery` ok. Mutation check in a copy of the tree (pending-not-winning, broken return link,
  completed-only filter dropped): 6 new tests fail as expected.
- Driven run (built till, throwaway data dir; plain sale, full refund, two partials, pending leg): `/journal` at
  1280x800, 800x480, 390x844, fa (RTL) and grayscale — five row kinds distinct, no horizontal scroll, refund link
  44 px under emulated coarse pointer and `body.kiosk`, link opens the original sale. Touch emulated only; dark theme,
  German pack and the post-tender mini journal were not checked visually (mini journal covered by
  `internal/ui/journal_test.go`).
- After the fix round (orchestrator, personally): `go test ./internal/data/ -run 'JournalRefund|PendingRefund|DeriveRefund'`
  ok, `go test ./internal/pages/ -run Journal` ok, gofmt clean, guard-i18n / guard-i18n-meta pass. Dev ran
  golangci-lint (0 issues) and the i18n, meta, RTL, help-topics/breadcrumbs/terms, compliance, fixed-dates and
  data-access guards.

Pre-flight: help topic `web/help/en/reports.md` updated; docs-shots deferred → #7069 (F5); en.json keys with
en.meta.json updated (English only); no reference/ page describes the journal list (n/a); README not affected (n/a);
follow-ups filed as #7069.

Deferred (→ #7069): refund history on sale detail, channel-orders chip, reports drill-down, pending lifecycle incl.
replica-made refunds (leg journal is till-local), refund-screen brand/last4 + over-refund, Stripe empty-currency log,
docs-shots.

Verdict: PASS after fixes.
