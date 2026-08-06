# 2026-08-06 — sales_by_day/top_items/payment_breakdown AI tools: snake_case JSON (ut-docs#322)

Card: [ut-docs#322](https://github.com/universaltill/ut-docs/issues/322) (p3, easy)
Branch: `fix/322-reports-ai-tools-snake-case`

## What shipped

`data.DailySales`, `data.TopItem`, and `data.MethodTotal` (`internal/data/pos_repo.go`)
had no `json:"..."` struct tags, so the `sales_by_day`, `top_items`, and
`payment_breakdown` "Ask your till" AI tools (`internal/pages/ask_api.go`)
shipped Go's default PascalCase field names to the model's tool-call JSON
marshalling — the identical defect class ut-docs#277 fixed for
`data.LowStockItem`/`stock_levels` earlier the same day.

- Added snake_case `json` tags directly to the three structs, following
  #277's precedent: `DailySales` → `day`, `count`, `total`, `tax_total`;
  `TopItem` → `name`, `qty`, `revenue`; `MethodTotal` → `method`, `count`,
  `amount`. Consistent with this file's other JSON-facing structs
  (`LowStockItem`, `DeptSales`, `AuditActionCount`).
- Added `TestReportsAITools_ReturnSnakeCaseJSON` (table-driven, one subtest
  per tool) to `internal/pages/ask_api_test.go`, mirroring #277's
  `TestStockLevelsToolReturnsSnakeCaseJSON`: marshals each tool's real
  result and asserts every snake_case key is present and no PascalCase key
  leaks through.
- Checked the 5th `askTools()` entry, `till_activity_summary` →
  `data.AuditActionCount`, for the same gap — already fully tagged, no
  action needed (confirmed by BA, re-confirmed independently by Reviewer).

## Independent review (Sonnet, fresh context — easy-tier card)

Ran build/vet/guards/full test suite itself, and independently
re-verified the TDD claim (reverted just the struct-tag hunk, confirmed
`TestReportsAITools_ReturnSnakeCaseJSON` genuinely fails with the
PascalCase keys for all three tools, restored the tags, confirmed green
again; confirmed the restored diff was byte-identical to before the
revert).

**Verdict: PASS, ship as-is.** No blockers, no bugs in the changed code.
Confirmed: tag names correct and consistent with sibling structs; no
other `askTools()` entry has the same gap; no SQL/money/i18n/file-write
rules triggered (pure struct-tag plumbing); no real client/shop name or
secret-shaped literal anywhere in the diff.

## Verification

- `go build ./... && go vet ./...`, `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh` — all clean.
- `go test ./...` — every package green except the pre-existing, unrelated
  `internal/issuereport` `TestSaveCleansUpDirectoryOnWriteFailure`
  (ut-docs#258, sandboxed root-run quirk — same flake noted in #277's own
  review record).
- TDD claim re-verified twice independently (once by the implementer, once
  by the reviewer): the new test fails against the untagged structs with
  real assertion errors quoting the leaked PascalCase JSON, and passes
  against the tagged ones.
- Not a visual/UI surface (no template, no user-facing string, no HTML) —
  no screenshot check applicable; noted explicitly rather than silently
  skipped.

## Safe-to-merge verdict

Yes. Independent review found no blockers and no scope gaps left
unaddressed.
