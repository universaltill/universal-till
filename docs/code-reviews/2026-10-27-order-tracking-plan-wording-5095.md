# Review: help names the plans that get order tracking (ut-docs#5095)

Since universal-till#2178 Local Plus is a paid plan, but customer order
tracking needs Shop and up (`entitlement.CapOrderTracking` = `PlanShop`).
The help still said order tracking is "part of the paid plans".

## Change
- `web/help/{en,de,tr,fa,ar}/customer-order-tracking.md`: summary and the
  first note name the Shop, Pro and Multi-location plans; the note adds
  Local Plus to the plans that show no QR.
- `web/help/{en,de,tr,fa,ar}/designer.md`: "only prints on a paid plan" →
  "on the Shop, Pro or Multi-location plan".
- `web/help/img/manifest.json`: the two topics' hashes. `make docs-shots`
  ran (124 passed); the topic PNGs are byte-identical, because these topics
  screenshot the app page (`/o/{token}`, `/receipt-designer`), not the prose.
  The run also re-rendered `multitill.png` in four locales with no related
  change; those were left out and the guard still passes.

## Review (Opus, fresh context; author Sonnet)
| # | sev | finding | outcome |
|---|---|---|---|
| 1 | info | The help still quotes the UI label "Order tracking QR (paid plans)" next to the new plan list | Accepted: the label's value lives in 4 core locales and 3 language packs; filed ut-docs#5107 |
| 2 | info | The free plan is "Lokal"/"Yerel"/… in the help prose but "Local (free)" in the UI | Pre-existing wording; not changed here |

Statements checked against code: `rank`/`Allows` (Shop, Pro, Chain pass),
`orderTrackingOffered` gates both the kiosk and the receipt QR,
`EffectivePlan` falls back to Local after an ended subscription or 7 days
unconfirmed. No other help sentence ties order tracking to "paid plans".

## Verification
guard-docs-shots, guard-help-drift, guard-help-topics,
guard-help-breadcrumbs, guard-compliance-claims, guard-competitor-naming,
guard-i18n: pass. `go test ./internal/manual/...`: ok.

## Not in this PR
- #5095 part 1 (language packs) was already done: `check-lang-pack-drift.sh`
  is clean for de/es/pt.
- #5095 part 3 (ut-cloud `localPlusMinTillVersion`) waits for a till release
  containing #2178 (v0.35.0 does not): ut-docs#5106.
- The UI label: ut-docs#5107.

Verdict: safe to merge.
