# Review: the till knows the `local_plus` plan (ut-docs#5067)

Without it the till rejected a `local_plus` entitlement block, so the
cheapest paid plan could never sync or back up.

## Change
- `internal/entitlement`: `PlanLocalPlus` between local and shop; `cloud_backup`,
  `cloud_sync`, `browser_catalog` now need Local Plus (mirrors ut-cloud
  `subscription.minTier`). New `CapOrderTracking` (Shop), mirroring ut-cloud's
  `OrderTracking`, so lowering `cloud_sync` does not hand Local Plus the
  customer tracking QR.
- `orderTrackingOffered` gates on `CapOrderTracking`.
- i18n: `subscription.plan.local_plus` ("Local Plus", a product name) and
  `subscription.cap.order_tracking` in every locale + meta; plan-name test
  covers the new plan.
- Help: customer-order-tracking and designer say Shop and up.

## Review (Opus, fresh context; author Sonnet)
| # | sev | finding | outcome |
|---|---|---|---|
| 1 | high | `orderTrackingOffered` used `cloud_sync`; lowering it to Local Plus would offer a Shop-only feature | **Fixed**: `CapOrderTracking`, test that Local Plus gets no QR |
| 2 | low | help said "paid plans" for order tracking | **Fixed** (English; translations keep structure) |
| 3 | info | old builds reject `local_plus` and stay on their cache; fails closed | No change |

## Verification
Entitlement, cloudsync, pages, data, web tests; guard-i18n, i18n-meta,
help-topics, help-drift pass.

## Follow-ups (not in this PR)
- Release the till, then set `localPlusMinTillVersion` in ut-cloud.
- `subscription.plan.local_plus` / `subscription.cap.order_tracking` in the
  `ut-plugin-language-*` packs (lang-pack-drift).
