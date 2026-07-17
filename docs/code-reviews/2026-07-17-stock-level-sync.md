# Code review — stock-level sync between tills (D3b)

**Date:** 2026-07-17
**Branch:** `feat/stock-level-sync`
**Ask (Farshid):** "Does the stock and inventory and selling sync between
till stations? this is so important."

## What already synced, and the gap

Sales always synced: every till's journal pushes to the primary (~30s,
offline-queued, idempotent) and the primary re-applies through the same
engine — so the PRIMARY's stock reflects every lane. The gap: replicas'
local stock only reflected their own sales; other tills' sales and the
primary's goods-in/adjustments never flowed back.

## Design: levels, not movements

- Primary serves `GET /api/sync/stock` (same till-bearer auth +
  `?have=` fingerprint short-circuit as the admin bundle): shop-wide
  on-hand aggregated per item/variant across locations.
- Replica, in the existing 30s pull tick, AFTER the admin apply and ONLY
  when its own push queue is empty (otherwise the primary hasn't counted
  our latest sales and a correction would briefly re-add sold stock):
  fetches levels and reconciles via **corrective `adjust` movements**
  through `RecordStockMovement` — inventory, the movement ledger, and the
  audit log stay consistent; nothing is silently overwritten. Keys the
  primary doesn't have are corrected to zero; idempotent (same bundle
  twice = zero corrections). Corrections are movements, not sales, so they
  never enter the push journal — no feedback loop.
- `sync.stock_version` cursor is a `sync.*` setting (never synced itself).

## Consequence: stock is primary-owned

A goods-received or manual adjustment entered on a REPLICA will be
overwritten by the next reconcile — by design, mirroring the catalog's
primary-wins model. The inventory page on a replica now shows the same
"this till follows the primary" banner as the catalog (reusing the
existing i18n keys). Follow-up if wanted: forward replica adjustments to
the primary instead of banning them.

## Tests / verification

- `TestStockLevelSyncConverges` (two migrated DBs): drifted replica (extra
  qty, missing item, drifted variant, primary-unknown item) converges to
  the primary's exact levels in 4 corrections; second apply is a no-op;
  the 4 corrections exist as honest `adjust` ledger rows.
- Wire layer mirrors the proven admin-pull pattern (same auth, same
  fingerprint protocol); full suite + guards green. Real 2-till LAN
  verification happens on Farshid's homelab pair.
