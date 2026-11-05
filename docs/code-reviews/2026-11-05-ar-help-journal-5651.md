# Review: ar help — drop the English "(Journal)" next to the Arabic label (ut-docs#5651)

**Date:** 2026-11-05 · **Lane:** lane:cloud-24 · **Complexity:** easy

## What shipped
- `web/help/ar/sell.md:23`: `سجل المبيعات (Journal)` → `سجل المبيعات`. The ar
  UI already calls the journal `سجل المبيعات` (`nav.journal`), so the
  parenthesised English word was noise for an Arabic reader.
- `scripts/ci/help-terms.txt`: two entries for `guard-help-terms.sh` —
  `(Journal)` is refused in ar and fa help and the guard names the locale's
  own `nav.journal` label. That way it can't come back.

## Review
- Independent reviewer: a different model from the author (Sonnet reviewing an Opus change), plus the tester pass.
- Re-verified: with the ar fix reverted, the guard fails on `ar/sell.md:23`
  (rc=1). With the fix restored, it passes (rc=0).
- Ran `guard-help-terms.sh`, `guard-help-terms_test.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-help-breadcrumbs.sh`
  and `guard-i18n.sh`: all pass. `go test ./internal/manual/... ./internal/httpx/... ./web/...` passes too.
- Scanned ar/fa help for other parenthesised English (by both the author and
  the reviewer). Everything else found is an acronym, a code, a legal name or a
  statute title: SKU, GBP, TSE, QR, GDPR, CRM, Finanzamt, TLS, EOD, PUK,
  "Employment (Allocation of Tips) Act". None is a duplicated UI label.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | `web/help/fa/sell.md:23` uses a bare English `Journal` as the nav label (fa UI: `دفتر فروش`). | Deferred and listed on #5651. Open PR universal-till#2385 (ut-docs#5644, another lane) rewrites this exact line, so editing it here would conflict. Follow-up card filed. |

Pre-flight: help topic — updated (ar text only; no screenshots affected); reference/README — n/a (no behaviour change); en.json/meta — n/a; review record — this file; follow-up — fa `Journal` line deferred to a Backlog card.

**Verdict:** approve.
