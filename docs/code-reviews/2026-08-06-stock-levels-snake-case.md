# 2026-08-06 — stock_levels AI tool: snake_case JSON (ut-docs#277)

Card: [ut-docs#277](https://github.com/universaltill/ut-docs/issues/277) (p3, easy)
Branch: `fix/277-stock-levels-snake-case`

## What shipped

`data.LowStockItem` (`internal/data/pos_repo.go`) had no `json:"..."` struct
tags, so every field shipped as Go's default PascalCase wherever the struct
was marshalled to JSON — against this repo's snake_case wire convention
(`universal-till/CLAUDE.md`). The most visible consumer is the `stock_levels`
tool exposed to the in-product AI assistant (`internal/pages/ask_api.go`),
which returns `[]data.LowStockItem` directly to `internal/ai`'s tool-call
JSON marshalling.

- Added snake_case `json` tags directly to `LowStockItem` (`item_id`, `name`,
  `sku`, `location_id`, `location_name`, `current_qty`, `reorder_level`,
  `lead_time_days`) — consistent with this same file's other JSON-facing
  structs (`DailySales`, `EODReport`, `AuditActionCount`, etc., all already
  tagged). Fixes every serialization site at once (the `stock_levels` tool
  and `/api/inventory/low-stock`'s JSON branch, via `internal/pos`'s
  `LowStockItem` type alias) rather than adding a one-off DTO for just the
  AI tool.
- Refreshed the `stock_levels` tool's `Description` to mention lead time —
  `LeadTimeDays` has been on the struct since ut-docs#85 but the description
  never described it.
- Extended `TestAskTools_RunFunctionsCallRealRepoMethodsWithParsedArgs` to
  assert the real `[]data.LowStockItem` is returned, and added
  `TestStockLevelsToolReturnsSnakeCaseJSON`, which marshals the tool's real
  result to JSON and asserts every snake_case key is present and no
  PascalCase key leaks through — a black-box guard on the actual wire shape.

## Independent review (Sonnet, fresh context — easy-tier card)

Ran build/vet/guards/full test suite itself, and independently re-verified
the TDD claim (stashed just the struct-tag change, confirmed the new test
fails with the PascalCase keys, restored it, confirmed it passes).

**Verdict: ship as-is.** No blockers, no bugs in the changed code. Confirmed
the fix is a plain type alias away from also correctly covering
`/api/inventory/low-stock`, and that Go `html/template` field access uses
struct field names (not json tags), so no template rendering is affected.

**2 MEDIUM scope-gap findings, filed as follow-up cards rather than folded
into this change (both pre-existing, neither a regression from this diff):**

- The sibling AI tools `sales_by_day`, `top_items`, and `payment_breakdown`
  (wired in the same `ask_api.go`) return `data.DailySales`, `data.TopItem`,
  and `data.MethodTotal` — all still untagged, so they emit the identical
  PascalCase defect class to the model today. Filed as
  [universaltill/ut-docs#322](https://github.com/universaltill/ut-docs/issues/322).
- `GetLowStock`'s JSON branch (`internal/pages/inventory_api.go:536`)
  responds with `{"items": ..., "count": ...}`, not the
  `{ "data": …, "error": null }` envelope `CLAUDE.md` mandates for all API
  responses. Filed as
  [universaltill/ut-docs#323](https://github.com/universaltill/ut-docs/issues/323).

**1 LOW, no action needed:** no doc under `ut-docs` describes the AI tools'
field-level JSON shape/casing at the schema level (checked
`architecture/ai-integration.md`, `reference/pos-acceptance-matrix.md`, and
the related review docs) — nothing there goes stale from this change.

## Verification

- `go build ./... && go vet ./...`, `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh` — all clean.
- `go test ./... -race` — every package green except the pre-existing,
  unrelated `internal/issuereport` `TestSaveCleansUpDirectoryOnWriteFailure`
  (ut-docs#258, sandboxed root-run quirk).
- TDD claim re-verified twice independently (once by the implementer, once
  by the reviewer): the new test fails against the untagged struct and
  passes against the tagged one.

## Safe-to-merge verdict

Yes. Independent review found no blockers; two MEDIUM findings are
pre-existing scope gaps in adjacent code, deferred to follow-up cards rather
than silently left unmentioned.
