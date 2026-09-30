# Code review: generate missing SKUs, with a boot backfill and a Catalog action (ut-docs#3097)

- **Date:** 2026-09-30
- **Ticket:** ut-docs#3097 (`complexity:medium`, `p1`, `source:user`)
- **Branch:** `feat/3097-generate-missing-skus`
- **Author:** Opus 5.5 (Dev subagent). **Reviewer:** Fable, run independently in an isolated worktree, per MODEL-ROUTING for a medium card.
- **Verdict:** safe to merge after the fixes below. No blockers.

## What shipped

- **`internal/data/item_sku_backfill.go`** adds three `CatalogRepo` methods: `CountItemsMissingSKU`, `PlanMissingItemSKUs` and `BackfillMissingItemSKUs`.
  - Plan and Backfill run one shared routine in a single IMMEDIATE transaction. It covers every item, active or not, whose `sku IS NULL OR trim(sku) = ''`, in the order category, name, id.
  - Each such item gets `nextItemSKU(tx, category)`, the #3087 generator, and a guarded `UPDATE`.
  - Plan rolls back, so the preview equals the commit on unchanged data.
- **`internal/app/item_sku_backfill_boot.go`** runs the backfill at boot, only when `sync.primary_url` is empty.
  - It runs after `ApplyReplicaIdentity`, so a till that has just joined a shop is already a replica and is skipped.
  - Errors are logged and never fatal. It logs one line when it assigns anything.
- **Catalog UI:**
  - `GET/POST /api/catalog/sku-backfill` (catalog_management; the POST is primary-only, 409 otherwise).
  - A "Generate missing SKUs (N)" icon button with a count badge, shown only on a primary while N > 0.
  - A `.show()` dialog that mirrors the barcode backfill (#1356).
  - Both routes are on the demo allow-list, and the cashier-403 gate test is extended.
- **i18n and help:**
  - Seven `catalog.sku_backfill.*` keys in en/ar/fa/tr.
  - A help bullet in `web/help/{en,de,ar,fa,tr}/catalog.md`, with the help-drift baseline counts shifted by exactly +1 bullet and +1 bold lead-in.
  - Language-pack follow-ups for de/es/pt.

The existing `sync_admin_version` and `sell_screen_version` triggers carry the new SKUs to additional tills. The cloud snapshot push is content-hash based, so it picks them up too.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix | The boot backfill is O(N·C + N²) index visits under one write lock, so a large legacy catalog (thousands of SKU-less items) could delay boot by seconds. | **Accepted risk, follow-up ut-docs#3280.** It stays synchronous on purpose: it runs once, before the server takes sales, so no sale can hit `SQLITE_BUSY`. A background run would trade a slower first boot for possible failed sales. There are no real shops yet. |
| S2 | should-fix | The German help named the confirm button "Diese SKUs zuweisen", but the de pack shows "Diese Artikelnummern vergeben". | Fixed: the help now uses the pack's labels. |
| N1 | nit | The preview (GET) takes the write lock, and it is reachable on a replica, where the confirm then gets a 409. | Accepted. Same as the barcode backfill; the lock part is folded into #3280. |
| N2 | nit | The dialog's `aria-labelledby` pointed at the button, which is absent on a replica and removed after Close. | Fixed: it now points at the dialog's own heading. The Tester found this independently. |
| N3 | nit | SQL `trim()` strips spaces only, while Go's `TrimSpace` also strips tabs and newlines. | Accepted. Theoretical for pre-#3087 rows. |
| N4 | nit | The count is a full `items` scan on each `/catalog` render. | Accepted. Milliseconds at realistic sizes. |
| N5 | nit | No audit row for the bulk SKU change. | Accepted. Consistent with the barcode backfill. |
| T1 | minor (Tester) | SKUs wrapped mid-code ("BAK-" / "0001") in the preview at 360 px. | Fixed: the SKU cell is `white-space: nowrap`. |
| T2 | gap (Tester) | No Playwright spec covers the button or dialog. | Accepted. Every insert path now fills a SKU and e2e has no direct DB access, so a spec cannot create a SKU-less item without a test-only endpoint. The handler tests and the Tester's driven run below cover it. |

## Verified beyond the unit tests

- **Reviewer:**
  - Checked that duplicate SKUs within one run are impossible (reads go through the transaction; ordering is deterministic), and that barcode collisions are excluded (`skuTaken` checks all four code tables).
  - Checked that the boot hook runs after migrations and replica identity, and that the change propagates to replicas and the cloud.
  - TDD re-verification by mutation:
    - Dropping the UPDATE fails the data tests.
    - Dropping `requirePrimary` fails the replica-409 test.
    - Forcing the button to always show fails the hidden-button tests.
    - Ignoring `sync.primary_url` fails `TestBackfillItemSKUsOnPrimary_SkipsOnReplica` ("replica boot changed SKUs: 0 items still missing, want 2"). The orchestrator re-ran this one mutation on its own.
- **Tester:** drove the real binary with Playwright (Chromium).
  - **Boot:** filled 3 items and then 6 (including an inactive item and a blank-string SKU) and logged the count; did nothing as a replica; a third boot logged nothing.
  - **Dialog flow:** the dialog is non-modal and the status bar stays clickable. The preview's SKUs equal what the commit wrote. Close removes the badge and refreshes the grid in place. A replica gets no button, and its POST returns 409. No console errors.
  - **Surfaces looked at:** en 1024×600 and 360×740, tr at both sizes, fa RTL 1024×600, ar RTL 360×740, dark theme, and inside the `/items` shell.
  - **Not looked at:** a real PIN login, other themes, the German UI (it lives in the pack), the "… N more" row, and real Pi hardware.
  - The related Playwright specs pass 14/14.
- **Gate:** `gofmt`, `go build`, full `go test ./...`, `golangci-lint` (0 issues), and every guard in the ci.yml `build` job are green.
  - shellcheck isn't installed in this container; no shell scripts changed.
  - After the template-only fix, the docs-shots surface hash was refreshed with `update-docs-shots-surface-hash.sh`: the dialog appears in no manual screenshot, since the seed catalog has every SKU filled.

## Deferred

- ut-docs#3280: generate the SKUs in one O(N) pass with a read-only preview.
