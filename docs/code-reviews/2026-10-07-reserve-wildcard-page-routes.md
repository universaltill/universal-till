# Code review — reserve core wildcard namespaces for plugin page routes (ut-docs#3791)

- Card: ut-docs#3791 (`p3`, `security`, `complexity:easy`), split from ut-docs#3789,
  unblocked by ut-docs#3786.
- Branch: `fix/3791-reserve-wildcard-page-routes`
- Author: Opus 5.5 (lane `lane:cloud-24`). Independent review: Fable, fresh context.

## What shipped

`internal/plugins/manifest.go` — `reservedPageRoutePrefixes` gains the core page
namespaces that have `{wildcard}` children: `/help`, `/orders`, `/journal`,
`/plugins`, `/refund`, `/invoice`, `/kitchen-display`. A pattern like
`GET /help/{topic}` leaves deeper paths (`/help/a/b`) unclaimed, so they fell
through to the `/` catch-all that renders plugin pages — a signed plugin could
render a page inside a core namespace. `#3789` had already limited the catch-all
to GET/HEAD, so the residual risk was shadowing, not method confusion.

`internal/plugins/page_route_validation_test.go`:

- `TestPersistManifest_RejectsReservedPageRoutes` — 7 new cases
  (`/help/a/b` … `/kitchen-display/a/b`), each asserting the transaction rolls back.
- `TestPersistManifest_AcceptsOrdinaryPageRoutes` — `/plugin/faq`, `/helpdesk`,
  `/plugin/x/y` added, so the widened list can't swallow legitimate routes
  (`/helpdesk` is the prefix-boundary case for `/help`).
- `TestReservedPageRoutePrefix` — `/help/a/b`, `/orders`, `/plugins/x/settings`
  now reserved; `/plugin/faq`, `/helpdesk` still not.
- `TestReservedPageRoutesCoverWildcardNamespaces` (new) — scans every non-test
  `.go` under `internal/` for literal `Handle`/`HandleFunc` patterns containing
  `{`, and fails if the first segment isn't reserved. This is the acceptance
  criterion "a test pins the list against the real mux registrations": a new core
  `{wildcard}` route can no longer be forgotten.

`ut-docs/reference/plugin-manifest.md` — the `route` row lists the new prefixes
(separate PR in ut-docs, same cycle).

Pre-check per the card: no published plugin declares a route under the new
prefixes (clones of all 13 `ut-plugin-*` repos: only `/plugin/faq` and
`/plugin/tax-uk-docs`), and nothing in-repo does either.

## TDD

Test-first: the 7 install cases, the prefix cases and the scanner were written
and watched fail against the unmodified list (the scanner named all 8 real
registrations: `help_page.go` ×2, `invoice_page.go`, `journal_page.go`,
`kitchen_display.go`, `order_status.go`, `plugin_settings_page.go`,
`refund_page.go`), then the list was extended and they passed.

Re-verified independently by the reviewer in a separate worktree: `manifest.go`
reverted to `HEAD~1` with the new tests kept → all three tests fail with the
claimed errors; restored → pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The scanner only sees *literal* patterns; `"POST "+discovery.ProofPath` (`sync_primary_watch.go:43`) and `sync_assets.go:128,146` are invisible to it. All three resolve under `/api`, already reserved, so nothing is missed today. | Fixed by documenting the limit in the test's doc comment, so a future variable-built registration under a new root isn't assumed covered. A repo-wide manual pass over all non-test `.go` (incl. `cmd/`, `scripts/`, `mobile/`) found first segments `/api /ui /o /help /themes /refund /plugins /plugin-icons /orders /kitchen-display /journal /invoice /{$}` only — all reserved, no missing root. |
| 2 | minor (pre-existing) | `common/state.go BuildMenu` / `ListMenuEntries` add a menu tile for every active page entry without consulting `ReservedPageRoutePrefix`, so a plugin installed *before* the check keeps a tile that now 404s at dispatch. True since #3786; this change widens the affected set. | Out of scope — filed as a Backlog card. No published plugin is affected. |
| 3 | observation | Multi-segment *literal* core children under unreserved roots (`/settings/menu`, `/catalog/tax-codes`, `/users/permissions`, `/open-orders/…`, `/recovery/…`) mean a plugin page at `/settings/vendor-x` still lands inside a core namespace. Same shadowing class, different trigger; deliberately outside this card and the new scanner. | Filed as a Backlog card. |
| 4 | nit | Reserving the *exact* routes (`/orders`, `/plugins`) is behaviourally redundant — the mux's exact pattern already beats the catch-all. | Accepted: consistent with the existing exact `/api` reservation, and it turns a silently-unreachable page into a clear install-time error. |

## Verified beyond the new tests

- `go build ./...`, `go vet ./internal/plugins`, `gofmt -l` clean.
- `go test ./...` (whole repo) green; `go test ./internal/plugins` green in the
  reviewer's own run too.
- Guards run locally: `guard-data-access`, `guard-core-neutral`,
  `guard-netaccess`, `guard-pipefail-grep-q` — all pass. No SQL, no locale key,
  no migration, no template touched, so the i18n/help/migration guards have
  nothing to act on; CI runs the full `build` job set.
- `golangci-lint` could not run in this container (its binary is built against
  go1.25, the module targets 1.27.1) — left to CI.
- Dispatch-time behaviour: `internal/pages/plugin_page.go:138` calls the same
  `ReservedPageRoutePrefix`, so the already-installed case is covered by the same
  one-line change; no second code path needed.
- `TestShiftsPage_OpenShiftShowsCurrentAndHistory` fails under a
  `-run 'Plugin|Page'` filter on this branch **and identically on `HEAD~1`**, and
  passes in isolation on both — a pre-existing order-dependent flake in the
  shifts page test, unrelated to this diff. Filed as a Backlog card.

No UI surface is touched (install-time validation + a Go error string), so no UX
pass and no `web/help/` update: nothing a shop owner sees changes. A plugin
developer reads `reference/plugin-manifest.md`, which is updated.

## Verdict

Safe to merge. Reviewer: "SAFE TO MERGE (no blockers, no majors)".
