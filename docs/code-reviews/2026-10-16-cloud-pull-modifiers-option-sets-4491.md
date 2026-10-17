# Review: cloud-master pull applies modifier groups, option sets and link lists (ut-docs#4491)

- **Date:** 2026-10-16
- **Card:** universaltill/ut-docs#4491. Rescoped this cycle: variants and barcodes were split out to ut-docs#4521.
- **Author:** Opus 5.5 (dev subagent), lane `lane:cloud-41`.
- **Reviewer:** Sonnet 5.5, independent subagent. The routed Fable review was refused (out of usage credits, HTTP 429), so this fell back to Sonnet as `MODEL-ROUTING.md` allows.

## What shipped

`internal/catalogpull` (ADR-0162 §4, contract `reference/contracts/cloud-catalog-feed.md`) now applies:

- **`modifier_group`**: `name`, `required`, `min_select`, `max_select`, `sort_order`, `active`, and `options` (the full ordered set, UUID ids).
  - Applied through the new `ModifierRepo.SaveGroupTx`. `SaveGroup` was split into prepare + tx write, following #4463's `SaveItemTx`.
  - Tombstone: `DeleteGroupIfExistsTx`, the same cascade as `delete_modifier_group`.
- **`option_set`**: `name`, `active`, and `values` (the full ordered set).
  - Applied through `SaveOptionSetTx`.
  - Tombstone: `DeleteOptionSetIfUnusedTx`. A set still in use is deactivated instead.
- **Link lists**: absent keeps the till's links, `[]` clears them, `null` refuses the row. Every id resolves through `cloud_links`, and an unknown id refuses the row.
  - category `modifier_group_cloud_ids` → `SetCategoryModifierGroupsTx` (inactive groups are allowed).
  - item `modifier_group_cloud_ids` and `modifier_opt_out_cloud_ids` → `ItemPatch.ModifierGroupIDs` and `ModifierOptOutIDs`.
  - item `option_set_cloud_ids` → `ApplyOptionSetsToItemTx`. It never generates variants, because variants arrive as their own rows (ut-docs#4521).
- **Dependency order** within a version group: tax codes → modifier groups → option sets → categories (parents first) → items → the rest.
- **`ApplierVersion` 1 → 2**, so tills that already pulled re-pull once from 0.
- `docs/data-model.md`: the "Cloud catalogue pull" section is updated.

No migration and no UI. There is no `web/help` topic for the pull.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A deactivated option set (tombstoned while in use) keeps its globally `UNIQUE` name, so the cloud re-creating a set with that name is refused. Its link is dropped, so it can be orphaned. | Accepted. The refusal is recorded. Follow-up: ut-docs#4522. |
| 2 | minor | An option or value id moved between groups or sets in one cloud version can be refused when the receiving row is applied first. | Accepted. The refusal is recorded. Follow-up: ut-docs#4522. |
| 3 | nit | Unique names refuse a cloud rename swap A↔B. The admin path has the same limitation. | Accepted. |
| 4 | nit | `Changed` lists `active` whenever the field is present. This is consistent with every other present-field entry in the audit rows. | Accepted. |
| 5 | nit | An item naming a refused group stays refused until the item changes again. This is the same behaviour as a refused parent category in #4463. | Accepted. |

The reviewer also confirmed:
- The non-Tx methods behave as before for directive and admin callers. `SaveGroup` writes `is_active` back with the value it just loaded.
- Every new error is wrapped with `%w`, so storage errors still abort the chunk and are not recorded as refusals.
- `SaveItemTx`'s group lists accept inactive groups (`requireGroupsTx`), which is consistent with the category path.

## Verified beyond the automated tests

- **TDD re-verified by the orchestrator:** with the two new kinds removed from `appliedKinds`, 7 new catalogpull tests fail. Restored, they pass.
- Gates (local):
  - `gofmt -l .` is clean.
  - `go build ./...` and `go vet` are clean.
  - `golangci-lint run ./...` reports 0 issues.
  - `go test ./...` passes (the same command as CI's test step).
  - `go test -race` passes for `internal/catalogpull` and `internal/data`.
  - A local `-race` run of `internal/cloudsync` hit the default 600s timeout while another race run shared the CPU. CI does not race that package.
  - The `ci.yml` build guards pass. Two can't run fully in this container: `guard-shellcheck-version` (no shellcheck binary) and `guard-deadcode-baseline`. The deadcode guard ran with `GOTOOLCHAIN=go1.27.1` and without the GTK desktop root; its only hits were in `internal/logging`, which this diff does not touch and which only that root uses. CI runs both guards in full.

## Verdict

Safe to merge.
