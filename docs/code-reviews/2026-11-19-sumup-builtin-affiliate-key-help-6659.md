# 2026-11-19 — Help: SumUp affiliate key is built in (ut-docs#6659)

Docs-only. `web/help/en/payments.md` no longer asks the merchant to enter an affiliate key, because SumUp plugin v1.9.0 builds it in. The App ID stays. A stale line said "no reader id = demo mode"; it now says a payment is declined without a reader unless Test mode is on.

The full review record (Opus 5.5, verdict approve after fixes) is in ut-plugin-payment-sumup `docs/code-reviews/2026-11-19-builtin-affiliate-key-6659.md`. Its M3 finding is fixed here.

Guards: help-topics, help-terms, help-breadcrumbs, help-drift, compliance-claims, competitor-naming and i18n all pass.

**Pre-flight:**
- Help topic: updated (English only, ADR-0160).
- Screenshots: n/a, text-only.
- `en.json`: n/a.
