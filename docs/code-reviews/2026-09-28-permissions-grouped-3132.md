# Review: Permissions page grouped, described, "Unlocks" (ut-docs#3132)

Date: 2026-09-28 · Lane: cloud-24 · Built by: Opus 5.5 · Reviewed by: Fable (independent)

## What shipped
- `/users/permissions` groups actions under headings (Sales, Catalog, Stock, Reports, Staff, Settings, Plugins, System; runtime "Other" fallback) from one Go table (`internal/pages/permission_groups.go`).
- One-line description per action (`permissions.action_desc.*`) and an "Unlocks: …" line listing the menu tiles / rail entries whose `VisibleIf` resolves to that action, derived from the `uislot` core tables through the same `menuPredicates` the menu uses (composite fiscal tiles only where the shop actually has them).
- Sticky action column, horizontal scroll inside the card only, logical CSS properties (RTL-safe).
- Guard test `TestPermissionGuard_EveryGatedActionIsOnThePage`: go/ast over `internal/pages/**` discovers gate helpers from `.Can(ctx,u,action)` to a fixed point and fails on any gated action without a `permission_actions` row or an en.json label, or with a computed name. Plus VisibleIf coverage, group-coverage and page tests; e2e `permissions-matrix-3132.spec.ts` (1024×600 en, 360×740 en/fa, sticky-column geometry).
- 33 new keys in en/ar/fa/tr; help topic `users.md` updated in 5 languages. No migration, no change to who may edit, the lockout guard or the POST handler.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | minor | `void` description/help claimed a manager-PIN void flow that doesn't exist; `price_override` described an unbuilt feature | Fixed: reworded in en/ar/fa/tr, help ×5, de/es packs |
| M2 | minor | Guard skipped method wrappers | Fixed (methods now discovered); func-literal wrappers documented as a known limit |
| M3 | minor | Composite fiscal tiles in "Unlocks" depend on the viewer's own `settings` grant | Accepted (display-only edge case) |
| N1 | nit | `/report-issue` (and `/users`) tile gated on `settings`, page on its own action | Pre-existing; follow-up ut-docs#3135 |
| N2 | nit | 1px border stub under the sticky column at group ends | Fixed |
| N3 | nit | `max-inline-size` on a table cell ignored | Removed |

Dev also found that `refund`, `void`, `price_override` and `cash_adjustment` are never checked by any gate (reviewer confirmed by instrumenting the guard: 207 sites, 18 actions, none of the four). Descriptions say so plainly; follow-up ut-docs#3134.

## Verified beyond unit tests
- Real app driven with Playwright: screenshots at 1024×600 and 360×740 in en, fa (RTL), ar, tr, light theme, looked at — pinned column stays put while role columns scroll, no page-level horizontal scroll, no overlap. Dark theme and real tablet hardware not checked.
- e2e spec verified to fail with `position: sticky` removed; guard test verified to fail with bogus actions in `authz.go`, `catalog/handlers.go`, and a computed name.

## Gate
gofmt clean, `go build`, `go vet`, `go test ./...` all ok, `golangci-lint` 0 issues, CI build-job guards pass (shellcheck not installed locally; no shell scripts changed), `make docs-shots` rerun (PNG diffs were sandbox font rendering only and restored; manifest hashes updated), e2e spec 3/3.

## Verdict
Safe to merge. Language packs: ut-plugin-language-de / -es follow-up PRs carry the 33 keys.
