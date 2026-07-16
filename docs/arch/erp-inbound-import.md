# Inbound data import (ERP / existing tills → Universal Till) — plan

> Companion to ADR-0014 (which covers OUTBOUND: sales/stock → ERP). This is the
> INBOUND half: getting an enterprise customer's catalog, prices and stock FROM
> their system of record (Ansar's LS Central / SAP / existing tills) INTO a
> Universal Till so it can sell on day one. Status: **planned**, partly built.

## Two distinct jobs

**1. Onboarding import (one-time).** Load the customer's full product master +
opening stock so a new till is ready to sell:
- products: name, SKU/PLU, **barcode/GTIN**, price, tax code, department/category,
  unit (each/kg — weighed goods), active flag
- opening stock per item (and per location/floor)
- optionally customers/loyalty

**2. Ongoing inbound sync (continuous).** Keep the till current with the master:
price changes, new/discontinued products, and stock adjustments made in their
ERP — pulled on a schedule or pushed by them.

## What we already have

`internal/catimport` parses CSV from **Loyverse / Square / generic** headers
(case-insensitive column synonyms, barcode normalisation incl. Sheets `.0`,
currency-decimal price parsing), and the `/import` page runs preview → import,
**idempotent** (existing barcode/SKU skipped), auto-creating categories and
attaching barcodes. Plus an **export** half (anti-lock-in). So the onboarding
path for a **file export** from any system already largely works — we extend the
format/column map for the customer's export.

## Plan — reuse the import seam, add connectors (same model as ADR-0014)

Fiscal/ERP specifics live in **plugins**; the core import seam stays generic.

- **Onboarding via file** — customer exports their catalog + stock (CSV/Excel
  from LS Central / SAP); we map its columns (extend `catimport` with an
  "ls-central"/"generic-erp" profile) and run the existing preview→import. Add
  **stock-level import** to the seam (catimport currently focuses on catalog;
  extend to set opening inventory + department/tax mapping).
- **Inbound connector plugin** (`integration` + `scheduler` type) — pulls from
  the ERP API (LS Central / Business Central **OData**, or SAP OData) on a
  schedule, transforms to the internal import shape, and feeds the **same
  idempotent import path**. This is the mirror of the outbound webhook
  connector: one reusable plugin per ERP family, endpoint/credentials/mapping in
  plugin settings. Needs a host seam so a plugin can write catalog/stock (today
  plugins are sandboxed to storage/http/log — an inbound connector needs a
  guarded "import" host function OR runs the pull and hands a file to the core
  import service; the core-service route is simpler and safer first).
- **Delta sync** — after onboarding, pull only changes (by updated-at/rowversion
  from the ERP) so it's cheap and frequent.

## Key decisions / caveats

- **Ownership.** If the ERP owns the catalog, the till's catalog is **downstream
  / read-mostly** — inbound sync overwrites local edits (ADR-0011 primary-wins
  analogue). Make that explicit per deployment so a floor manager's local change
  isn't silently clobbered without warning.
- **Barcode/GTIN is the join key** across systems (same key the product-search
  network backlog uses).
- **Offline-first unaffected** — import/sync is an online, best-effort background
  concern; selling never depends on it.
- **Scale** — hypermarket masters are 100k+ SKUs; import must page/stream and
  not block the UI (ties to the E7 scale-hardening item).

## Sequence

1. Extend `catimport` with stock-level + department/tax mapping and an ERP/
   generic profile (onboarding via file — works for Ansar's export today).
2. Core **import service** callable by a connector (safer than a plugin write
   host function).
3. **Inbound connector plugin** (LS Central / SAP OData pull → import service),
   scheduled, delta-based — needs Ansar IT's API details.
4. Reconcile with outbound (ADR-0014) so a two-way sync doesn't loop
   (don't re-export a sale's stock change that came from an inbound adjustment).
