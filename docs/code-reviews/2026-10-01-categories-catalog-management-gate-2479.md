# Code review — /categories gates on catalog_management (ut-docs#2479)

**Date:** 2026-10-01 · **Lane:** lane:cloud-54 · **Author:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped
- `internal/pages/categories_page.go`: `requireManager` (POST `/api/categories`, `/{id}`, `/{id}/active`, `/reorder`) and `requirePageManager` (GET `/categories`) now check `catalog_management` instead of `settings`. This matches the designer's category routes, `/items`, and `/api/catalog/*`, which follows the product owner's decision of 2026-09-28 on #2479/#2483.
- `internal/pages/designer_categories_api.go`: comment updated (no code change).
- Tests:
  - `TestCategoriesPage_GatesOnCatalogManagementNotSettings` splits the two permissions on `manager` both ways.
  - `internal/db` `TestSeededRoles_SettingsAndCatalogManagementCoOccur` checks that every migration-seeded role holds both permissions or neither. That parity is why no seeded role's access changes.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `ut-docs/architecture/ai-product-photo.md` still calls `settings` the category page's gate | Fixed in a companion ut-docs PR this cycle |
| 2 | minor | No review record in the WIP snapshot | This file |
| 3 | nit | The refusal subtest used `t.Fatalf`, so one regression hid the rest | Switched to `t.Errorf` |
| 4 | nit | Historical review records (`2026-09-17-catalog-admin-server-gate-2357.md`, `2026-09-23-designer-wysiwyg-2174.md` F6) describe the old gate | Left as dated history. F6 of #2174 is resolved here |

The reviewer checked the following:
- All five `/categories` routes go through the new gate.
- No other file registers them (`demo_mode.go` only lists them).
- The hub link (`uislot.CoreItems`, under `/items`, `VisibleIf: catalog_management`) is now consistent with the route gate. Before this change they did not match.
- The custom-roles sync (#3165) maps grants generically, so it needs no change.
- Help prose ("manager only"), `README.md` and `permissions.action_desc.catalog_management` are still accurate.

## Verified
- Reviewer re-ran the TDD claim. Reverting the two gate lines makes both subtests fail (`403, want 200` / `200, want 403`). Restoring them makes the tests pass.
- Parity test, reasoned against the seeds, fails in each of these cases:
  - cashier gains `settings`;
  - manager loses `catalog_management`;
  - cashier gains both.
- Checks run:
  - `gofmt -l .` is clean;
  - `go build ./...` and the full `go test ./...` pass;
  - `golangci-lint` on the touched packages reports 0 issues;
  - guards: data-access, i18n, help-topics, help-drift, kiosk-engine, no-showmodal, page-http-error, compliance, competitor-naming, core-neutral.
- No visual surface changed, so no screenshot was taken. The page renders identically for every seeded role.

## Verdict
Safe to merge.
