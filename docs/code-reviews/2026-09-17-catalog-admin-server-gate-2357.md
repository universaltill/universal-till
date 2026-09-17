# 2026-09-17 — catalog admin GET routes server-side gate (ut-docs#2357)

## What shipped

Follow-up from ut-docs#2312, which gated every *mutating* route under
`/designer`, `/items`, `/api/catalog/*`, `/api/buttons/*` with the
`catalog_management` permission and hid the `/designer`/`/items` nav
tiles via `VisibleIf: "catalog_management"` — but left the GET *page*
routes themselves (`/designer`, `/items`, `/catalog`, `/modifiers`,
`/catalog/option-sets`) with no server-side gate, only the nav tile
hidden. A cashier typing one of these URLs directly still got the full
page (information disclosure only — every mutation reachable from those
pages was already gated).

Adds the missing gate to all five routes, matching the existing
`canPerform(...)` + `httpx.RenderError(..., http.StatusForbidden, ...)`
pattern `tax_codes_page.go`/`locations_page.go` already use for their own
GET handler — deliberately not `common.LocalizedError` (a bare
`http.Error` body meant for HTMX/API fragment responses, not a full
themed page). `/designer` and `/items` (package `pages`) call the
existing package-level `canPerform` helper directly; `/catalog`,
`/modifiers`, `/catalog/option-sets` (package `catalog`, which cannot
import `pages.canPerform` without an import cycle) get a new
`requireCatalogManagementPage` closure in `handlers.go`, sharing its
boolean check (`catalogManagementAllowed`) with the existing
`requireCatalogManagement` (API/mutation) closure #2312 already added.

## A real gap the gate additions surfaced

Adding the gates broke 16 pre-existing tests across 5 files
(`designer_page_test.go`, `items_page_test.go`, `help_hint_test.go`,
`pos_alert_no_duplicate_test.go`, `ui_smoke_test.go`) that hit these
routes with no authorized session — they predate any permission check
here and are about rendering, not permissions. Fixed with
`t.Setenv("UT_AUTH", "off")` on each of the 16, rather than changing the
shared test-deps helpers (`newDesignerTestDeps`, `newMenuPageTestDeps`),
which are used by several other test files unrelated to this card
(`newMenuPageTestDeps` alone backs 10 files, including one that tests a
different permission gate) — a helper-level default risked side effects
a per-test override doesn't.

## Independent review (Sonnet subagent, fresh context per easy-complexity routing)

Ran entirely in an isolated worktree; did its own TDD re-verification
(reverted the three production files, confirmed the 4 new permission
tests fail with real output; restored, confirmed green) and its own full
gate (`gofmt`, `go build`, `go vet`, `go test ./...` for the whole repo,
`golangci-lint`, `guard-page-http-error.sh`, `guard-i18n.sh`,
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-help-topics.sh`,
`guard-compliance-claims.sh`) — all clean, including a genuine full-repo
`go test ./...` with zero failures.

Independently judged both open design questions and found them sound:

- **Gating `/items` itself doesn't newly restrict `/categories`/
  `/inventory` reachability**: both keep their own separate, untouched
  gates (`/categories` on `"settings"`, `/inventory` ungated), reachable
  by their own direct URLs regardless of `/items`'s own gate. `/items`'s
  nav tile was already `catalog_management`-only since #2312; this only
  closes the direct-URL bypass of that existing decision.
- **Fixing the 16 tests via per-test `t.Setenv` rather than the shared
  helpers**: confirmed correct — the count matches exactly (5+8+1+1+1),
  and the shared helpers are used well beyond this card's own files.

**Findings — fixed:**

1. (Medium) `TestItemsPage_NoSectionIsDisabled`
   (`internal/pages/items_page_test.go`) is a 17th test with the same
   root cause as the 16 already fixed, missed because it doesn't turn
   red: it asserts the *absence* of `aria-disabled="true"`/"Coming soon"
   markers, and the new 403 error page trivially satisfies "absence" too
   — the test kept passing while providing zero real coverage. **Fixed**:
   added the same `t.Setenv("UT_AUTH", "off")` its 16 siblings got;
   re-verified it now actually exercises the real items page (confirmed
   the 403-page false-pass no longer applies).

**Findings — noted, not fixed (legitimately out of scope):**

2. (Low) Several `GET /api/catalog/*` fragment endpoints (`lookup`,
   `modifier-groups-panel`, `variant-options`, `item-variants`,
   `icon-state`, `barcode-backfill`) still carry no `catalog_management`
   gate at all — same disclosure class as this card's own bug, just for
   HTMX/API fragments instead of full pages, reachable by any
   authenticated session including a cashier. Out of scope for this
   card's title/AC (the 5 GET *page* routes); worth a follow-up Backlog
   card.

## Verified beyond automated tests

- `go test ./...` for the whole repo, twice independently (Dev, then
  Reviewer subagent) — clean both times, no failures in any package.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- `bash scripts/ci/guard-page-http-error.sh` — confirms no page-route
  handler in this diff uses a bare `http.Error`/`LocalizedError` (the
  exact distinction this diff is careful about between the page routes
  and the pre-existing mutation routes).
- `bash scripts/ci/guard-i18n.sh` — clean; confirmed no locale files
  touched (reuses the existing `common.error.manager_or_admin_required`
  key, no new key needed).
- New tests assert the 403 error page still renders the full themed
  layout with the nav rail intact (`class="nav"` present), matching
  ut-docs#1458's own precedent that a page-route 403 must never be a
  bare, rail-less body.

## Safe to merge

Yes. Root-caused correctly, TDD-verified twice independently (Dev,
Reviewer), the one real test-coverage gap the gate additions surfaced
was found by independent review and fixed, and the design scoping
question (whether gating `/items` regresses `/categories`/`/inventory`
access) was checked and confirmed not to regress anything.

## Deferred

- File a follow-up Backlog card for the ungated `GET /api/catalog/*`
  fragment endpoints (finding 2 above) — same root cause, different
  surface, out of scope for this card's title.
