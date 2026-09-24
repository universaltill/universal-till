# Review — `tip_recipient` on the `fiscal.sign.ask` payload (ut-docs#833)

**Date:** 2026-09-24 · **Reviewer:** independent Fable subagent (same review as the plugin side)

**What shipped:** `fiscalSignAskPayment.TipRecipient` (`tip_recipient`, omitted on an untipped
payment; an unset recipient on a tipped one is sent as `employee`, the default `CompleteSale`
persists). Contract `fiscal-sign-ask` 1.9.0. The consumer is `ut-plugin-tax-de` v0.7.0, which
places an employee's tip in the 0%/`NULL` bucket and a business's tip as turnover.

**Findings touching core:** the reviewer confirmed the field reaches all three dispatch sites
(tender, refund, inventory return) through the single `buildFiscalSignPayload`, and that the
tender path applies `charge.policy.ask`'s default before dispatch. A real core inconsistency
(whether `Amount` includes the tip: stored path yes, reader-reported path no) is out of scope
and filed as **ut-docs#2571**. Full record: `ut-plugin-tax-de/docs/code-reviews/2026-09-24-tse-tip-discount-833.md`.

**Tests:** `TestFiscalSignPayload_TipRecipientOnTippedPayments` (written first, failed to
compile without the field); full `go test ./...` green. **Verdict:** safe to merge.
