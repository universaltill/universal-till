# Review — my. Inventory node, till side (ut-docs#3643)

**Date:** 2026-10-18 · **Built by:** Opus 5.5 (`lane:cloud-41`) · **Reviewed by:** Sonnet 5.5 (independent, different model; Fable refused — out of credits) · **Branch:** `feat/3643-inventory-node`

Companions: the universal-till, ut-cloud and ut-my-shop branches of the same name, each with its own record. Same three-repo shape as the Registers node (ut-docs#3642). Stock-take split to ut-docs#4636.

## What shipped here
- `internal/cloudsync/cloudsync.go`: additive, hashed snapshot field `stock_levels: [{item_id, variant_id, location_id, qty}]` from `ListStockLevels` (real rows; variant rows carry the parent `item_id`, item rows `variant_id: ""`), sorted for a stable hash, capped at 50 000; a read error omits the field. Summed per-item `qty` unchanged. `adjust_stock` dispatch reads optional `location_id`/`variant_id` (non-string → failed).
- `internal/pages/cloudsync_wire.go` `cloudAdjustStock`: chosen location must exist and be active; a named variant must belong to the item; item must exist and track stock; an item with an active variant needs a variant (ADR-0043). Absent fields keep the old location pick. Records a variant movement with item_id NULL, publishes the event with `VariantID`, and calls `NudgeLink(fleetlink.ScopeStock)` like the local adjust (it never did before).
- `internal/data/catalog_repo.go`: `CatalogRepo.HasActiveVariants`.
- Behaviour change: an `adjust_stock` from any cloud on an untracked or unknown item now fails instead of writing a movement.

## Findings (independent review: Sonnet 5.5 — Fable 5.1 was refused for lack of usage credits; Tester/UX gate: Opus 5.5 driven run)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | ADR-0043: nothing refused an item-level `adjust_stock` (no `variant_id`) on an item with active variants — it would create a parent row that double-counts. | **Fixed** in both places: till `cloudAdjustStock` fails `item has variants; choose a variant` (new `CatalogRepo.HasActiveVariants`), cloud endpoint `409 variant_required`; my. maps the code; key in all 10 locales. Tests watched fail first; orchestrator re-verified the till test (check disabled → fails, restored → passes). |
| 2 | major (UX) | my. table broke at 360px (names split mid-word, Adjust/form clipped in de-de, chip + button pushed the panel sideways). | **Fixed:** `break-word`, SKU nowrap, chip above the button, SKU column folded under the name below 480px. |
| 3 | medium | `parseDelta` read "1,000" (en) / "1.000" (de) as 1. | **Fixed:** only the locale's decimal separator is accepted (plus `٫`, and `.` in ar/fa); a grouping or second separator is refused. 8 tests. |
| 4 | minor | Cloud accepted an inactive variant. | **Fixed:** `404 variant_not_found`. |
| 5 | low | Escape closed the form only from the inputs; Adjust buttons misaligned (CSS specificity). | **Fixed:** handler on the `<form>`; selector fixed. |
| 6 | low | Viewer's read-only note rendered after the list; "nothing tracks stock" empty state had no next step. | **Fixed:** note above the table; empty text now says to turn on Track stock in the Catalogue (10 locales, each locale's form of address). |
| 7 | minor | `invalid_qty_delta` / `invalid_reason` unmapped in my. | **Fixed:** mapped to the form's own messages. |
| 8 | nit | Registers help said "under Inventory" (reads as nested). | **Fixed** in en/de/tr. |
| 9 | minor | `stock_levels` (≤ 50 000 rows) is re-served in full on every `GET …/manage`; a huge till could push the snapshot over 16 MiB. | **Deferred** → ut-docs#4639. |
| 10 | minor | my. fetches `GET …/items/{id}` once per variant-bearing item on each loaded page (N+1); a failed fetch keeps that item's rows disabled until reload. | **Deferred** → ut-docs#4639. |

## Verified beyond the unit tests
- Reviewer TDD re-verification in throwaway worktrees: till inactive-location check, cloud `not_reported` (no `stock_levels`) check, my. required-reason check — each disabled → its test fails; restored → passes.
- Driven run of the my. demo + routed production fixtures (Tester/UX): 1280×800, 1024×600 (incl. touch emulation), 360×740 × en-gb, de-de, fa-ir; list per location, location switch, search, variant rows (`variant_id` sent), Adjust with/without reason, Waiting → Applied / Failed, viewer, not-reported, no active location, no match. Screenshots read. **Not checked:** real touch hardware, a real cloud↔till round trip, dark theme, load-more paging, screen reader.
- Gates: universal-till `gofmt`, build, vet, `golangci-lint` 0, `go test ./...` (all 98 packages), `-race` on `internal/cloudsync` and the cloud-adjust subset of `internal/pages` (the whole `pages` package under `-race` exceeds 45 min on this 4-CPU box; CI runs it), guards data-access / core-neutral / i18n / kiosk-engine; ut-cloud `scripts/ci/verify.sh` exit 0; ut-my-shop `npm run check` (1352 tests) + `guard-cloud-keys` + `guard-till-parity`.

## Verdict
Safe to merge in order: ut-cloud → universal-till → ut-my-shop (my.'s `guard-cloud-keys` reads ut-cloud `main`; the cloud refuses adjustments until a till reports `stock_levels`, so the order is safe).

## Deferred
Stock-take flow → ut-docs#4636. Manage payload size + N+1 variant fetch → ut-docs#4639.
