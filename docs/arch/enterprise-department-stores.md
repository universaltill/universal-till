# Enterprise & department-store support

Motivation: a prospective customer runs several **department stores in Dubai**.
Department stores stress a POS differently from a corner shop — many
departments, many tills running at once, large catalogs, layered staff, and
head-office consolidation across branches. This doc maps those needs to what
Universal Till already has and lays out the increments to close the gaps.

## What a department store needs (and where we stand)

| Need | Status | Notes |
| --- | --- | --- |
| **Departments** as a first-class dimension (menswear, electronics, grocery…) — sales, targets, staff, stock rolled up per department | ⚠️ Partial | Categories are already **hierarchical** (`categories.parent_id`), and items link to a category. A top-level category *is* effectively a department; we just don't report or manage along that axis yet. |
| **Many tills at once** in one store | ✅ Have foundation | LAN sync (ADR-0011): one primary till + replicas over the LAN, per-till receipt prefixes, conflict-free additive stock. Scales to a floor of registers with no server. |
| **Per-department / per-till reporting** (Z-report per department, per register) | ⚠️ Partial | EOD Z-report + TopItems are store-wide. Sale lines carry `item_id`, and sales carry till provenance (migration 015), so both axes are derivable. |
| **Layered staff & permissions** (floor staff, department manager, duty manager, store manager, head office) | ❌ Gap | `users.role` is only `cashier\|manager\|admin`. No department scoping, no granular permissions. |
| **Large catalog / high SKU count** | ⚠️ Watch | SQLite handles 100k+ SKUs fine; the perf work (008) + related-items indexing help. Needs a scale test + catalog search paging audit. |
| **Concessions / consignment counters** (a brand runs its own counter, settled separately) | ❌ Gap | No vendor/concession attribution on sale lines or settlement reports. Common in Gulf department stores. |
| **Multi-store / head-office consolidation** across branches | 🔜 Roadmapped | Cloud tier (ADR-0013 layer 3): claimed stores roll up to an account; multi-store cloud sync is the first paid unlock. LAN sync stays per-store. |
| **Price/promotion management at scale** (per-department promos, markdowns) | ⚠️ Partial | Promotions exist; not department-scoped. |
| **Tax / fiscal** (UAE 5% VAT, tax invoices) | ✅ Mostly | Tax-inclusive/exclusive engine, VAT invoices (migration 016). UAE specifics (TRN on invoice, simplified vs full tax invoice) need a checklist. |
| **Localization** (Arabic RTL for Dubai staff/receipts) | ⚠️ Partial | i18n + RTL framework exists (fa proves RTL). **Arabic (`ar`) locale not yet shipped**; receipts need RTL bitmap mode. |

## Increment plan

**E1 — Departments as a reporting dimension (no schema change).** Treat each
item's top-level category as its department; add per-department sales to the
Reports page and the EOD Z-report. Immediately demoable, zero migration. *(This
increment ships alongside this doc.)*

**E2 — Explicit department model.** Promote departments to a first-class entity
(either a `is_department` flag on top-level categories or a `departments` table
categories belong to), with a department manager assignment and per-department
targets. Enables department dashboards.

**E3 — Roles & permissions for a store hierarchy.** Replace the 3-role enum with
role + optional department scope + a permission set (open register, apply
discount over X%, void, refund, manage catalog, view reports). Department
manager = manager scoped to a department. Keep the current roles as presets so
nothing breaks.

**E4 — Per-till / per-register reporting & reconciliation.** Use the sale
provenance (015) to produce a Z-report per till and a cash-up per register;
surface the fleet of tills (the Tills page already lists them).

**E5 — Concessions / consignment.** Attribute sale lines to a concession vendor
(counter), with a settlement report per vendor. A `plugin`-friendly extension
point (vendor tag on lines + a report type).

**E6 — Arabic locale + RTL receipts.** Ship `ar.json` and RTL thermal receipts
(bitmap render) for the Dubai market — folds into the multilingual rule.

**E7 — Scale hardening.** A 100k-SKU + 20-till load test; catalog search paging;
report query indexes. Confirm the primary-till model holds a busy floor.

**E8 — Multi-store consolidation.** Head-office view across branches on the
cloud tier (ADR-0013 L3) — the paid unlock that turns "a few department stores"
into one managed estate.

## Positioning

We already have the hard parts a department store cares about: offline-first
checkout that never blocks, multi-till on a plain LAN with no server, hierarchical
catalog, VAT + tax invoices, and multilingual/RTL. The gaps are mostly
*reporting and org-structure* layers on top of a sound core — additive, not a
rebuild. E1 lands now; E2–E8 sequence toward a full enterprise offering.
