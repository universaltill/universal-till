# Review: `catalog.identify.confirmed` targeted event (ut-docs#4007)

ADR-0121 amendment 2026-10-09 **R2b**. Built by Opus 5.5, reviewed by Fable
(different model, MODEL-ROUTING medium).

## What shipped

- `identifySlots.take` returns the plugin that owned the slot.
- `learnIdentifyPick` resolves the item, stores the photo
  (`storeIdentifyPick`, the old body: true only when the `ai_ref` was
  written), deletes the slot file, then `dispatchIdentifyConfirmed`:
  - skipped silently unless the slot's plugin subscribes to
    `catalog.identify.confirmed` (manifest hook);
  - `view:inventory` via `CheckPermissionAuditOnce` (first denial audited);
  - `sku` is the item's own SKU read by `item_id` (`CatalogRepo.GetItem`);
  - `EventBus.AskPlugin(pluginID, …)` — targeted, checks `events:receive`,
    answer discarded. Ordinary call slot (no `.ask` suffix; asserted in
    `TestIsSalePathEvent`) and the wasm runtime's ordinary event deadline.
- Payload `{job_id, item_id, sku, stored}`. Not sent when no slot was
  taken or the code resolves to no item.
- Docs: `ut-docs/reference/plugin-views.md` (the seam), `docs/plugin_guidelines.md`,
  event row in `docs/arch/plugin-integration-roadmap.md`.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `sku` was `ResolveBase(...).SKU`, which the production resolver sets to the picked code (a barcode; blank for `item:<uuid>`), not the item's SKU the docs promised | Fixed: read by `item_id`; test with a barcode pick |
| 2 | major | `view:inventory` checked (and denial audited) before checking the plugin hooks the event: every pick of a #3873-only identify plugin wrote a `permission_denied` row | Fixed: hook check first, then `CheckPermissionAuditOnce`; tests for both |
| 3 | minor | Deadline/slot class untested; the hang test only proves off-request | Fixed in part: `catalog.identify.confirmed` added to `TestIsSalePathEvent`'s ordinary list. The deadline itself is the wasm runtime's (`timeoutFor`), already covered there |
| 4 | minor | Slot file deleted only after the dispatch | Fixed: removed before dispatch |
| 5 | minor | One ordinary slot per plugin on mobile: a slow handler delays the next identify | Accepted (ADR: "ordinary call slot"); documented "return promptly" |
| 6 | nit | Guidelines said "stores … and then sends" though `stored:false` is sent too | Fixed ("tries to store") |
| 7 | nit | `event_dispatch` audit row per pick | Accepted: existing bus behaviour |
| 8 | nit | WebP → `stored:false` untested | Fixed: added to `ConfirmedStoredFalse` |
| 9 | nit | No separate events reference file in ut-docs | Accepted: plugin-views.md + guidelines + roadmap table are the event docs |

Shutdown: reviewer confirmed `AsyncWork` drains (20 s) before plugin
manager close and the wasm call is bounded ≤ 10 s, so the learning
goroutine cannot outlive the drain.

## Verified beyond the automated run

- Mutation checks (each reverted after): dropping the `view:inventory`
  check, broadcasting via `Publish`, forcing `stored` true, dropping the
  hook check, sending the picked code as `sku` — each fails a `_4007` test.
- `go test ./...` green (before the review fixes); `internal/pages` +
  `internal/plugins` re-run after them; `_4007` tests ×5 under `-race`.
- e2e `camera-error-branching-ai-identify-1559.spec.ts` 6/6.
- No UI surface changed (no templates, no locale keys) — nothing to look at.
- Local-only guard failures, environment not code: deadcode-baseline (no
  GTK headers → desktop root skipped), shellcheck version, store-private
  (BSD awk). golangci-lint can't load Go 1.27 locally; CI runs it.

## Verdict

Safe to merge once CI is green. Merge the ut-docs reference PR first.
