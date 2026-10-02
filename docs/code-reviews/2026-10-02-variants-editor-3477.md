# Review — my. Variants editor and the `save_item_variant` directive (ut-docs#3477)

- **Date:** 2026-10-02 · **Lane:** lane:local
- **Repos:** universal-till `feat/3477-save-item-variant`, ut-cloud `feat/3477-save-item-variant`, ut-my-shop `feat/3477-variants-editor`, ut-docs `docs/3477-variants-spec`
- **Built by:** Claude Opus 5.5 (Dev subagents) · **Reviewed by:** Claude Fable 5.1 (independent subagent)
- **Owner ask (2026-10-02):** "still I cannot add or edit variants".

## What shipped
- **Till:** main-till-only `save_item_variant` {item_id, variant_id, create?,
  name?, sku?, price_minor?, active?, barcodes?}; `SaveVariant` in one
  transaction (variant row, full barcode set via a new replace helper with
  `ensureBarcodeAvailable`, price history, inventory row on create), ≤ 100
  variants per item, auto `VAR-…` SKU when blank, item and variant SKUs one
  namespace; idempotent for a re-served create; audit row.
- **Cloud:** `POST …/items/{id}/variants`, `PATCH …/variants/{vid}`;
  cloud-minted variant id; validators incl. `checkSKUFree` now covering
  variant SKUs; version gate 0.30.16 before validation; pending overlay per
  variant (own `item_variant` sync state); changes carry `item_id`.
- **my.:** editable Variants tab — add, edit (name, SKU, price, barcodes),
  deactivate/activate (never delete), per-row sync state and refusal reason,
  translated errors, pre-gated on 0.30.16; Open/Retry on a variant change
  opens the item on its Variants tab.
- **Spec:** `reference/manage-shop-catalog-api.md` §1.3, §1.6, §2.3, §2.5b,
  §2.9–§2.11, §3.11, §4.4, §5.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | medium | Change-list Open did nothing for `item_variant` | **Fixed** (cloud `item_id` on changes; my. opens item on Variants) — tests red first |
| M2 | medium | Editing a not-yet-reported variant with auto SKU refused client-side | **Fixed** (SKU required only when on the till) — test red first |
| M3 | low | `checkSKUFree` O(catalog) on every SKU save | Card ut-docs#3490 |
| L1 | low | Focus lost after Cancel / Edit save | **Fixed** (back to the opener) — test |
| L2 | low | No pre-gate on 0.30.16 | **Fixed** — test with a 0.30.15 main till |
| L3 | low | SKU placeholder truncated in de/fr/tr | **Fixed**: text moved to the field hint |
| L4 | low | Barcodes not checked against pending creates | Card ut-docs#3490 |
| L5 | low | Cloud vs till price cap mismatch (pre-existing) | Card ut-docs#3490 |
| L6 | low | Till has no name-length bound (same as SaveItem) | Accepted (cloud enforces 120) |

## Verified beyond unit tests
- Reviewer re-verified TDD in separate worktrees: barcode-conflict check
  off → till test fails; variant SKU walk off → two cloud tests 202 instead
  of 409; VariantsEditor stubbed → 12 of 23 tests fail.
- Driven run (demo build, Playwright): Latte → Variants → Add variant →
  name + price → Save → new row "Waiting for till", Edit/Deactivate per row;
  1440×900 en-GB and fa-IR, 390×844 de-DE. Screenshots looked at (found the
  SKU placeholder truncation, fixed). Not looked at: dark theme, a real till
  applying the directive (tests only; needs till ≥ 0.30.16).
- Gates: till vet + data/cloudsync/pages tests + data-access guard; cloud
  vet + claims/api/tests; my. `npm run check` (619 tests).

## Verdict
Safe to merge: universal-till and ut-cloud first, ut-docs spec, then my.
A till release (0.30.16) is needed before shops can use it.
