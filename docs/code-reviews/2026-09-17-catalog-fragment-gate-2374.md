# 2026-09-17 — catalog GET fragment routes server-side gate (ut-docs#2374)

## What shipped

Follow-up from ut-docs#2357 (which gated the five full-*page* GET routes
under `internal/pages/catalog/handlers.go`, and explicitly deferred this
exact gap as finding 2, "out of scope for this card's title") and
ut-docs#2312 (which gated every *mutating* `/api/catalog/*` route). Six
read-only `GET /api/catalog/*` HTMX/API fragment endpoints carried no
`catalog_management` gate at all:

- `GET /api/catalog/lookup`
- `GET /api/catalog/modifier-groups-panel`
- `GET /api/catalog/variant-options`
- `GET /api/catalog/item-variants`
- `GET /api/catalog/item/icon-state`
- `GET /api/catalog/barcode-backfill`

Any authenticated session — cashier included — could reach catalog,
modifier, variant and barcode data directly, no `catalog_management`
permission required. The fix adds `if !requireCatalogManagement(w, r) {
return }` as the first line of each of the six handlers, reusing the
existing closure #2312 already wired up for every mutating route in this
file (plain `common.LocalizedError` 403 response — these are HTMX/API
fragments, not full pages, so `requireCatalogManagementPage` /
`httpx.RenderError` don't apply here, matching the distinction #2357's
own review record documents).

Two new tests in `catalog_management_gate_test.go`, following the
existing `newCatalogMuxRealSession` pattern (real migrated
`role_permissions` schema via `internal/db.Open`, not a hand-rolled
fixture):

- `TestCatalogHandlers_CatalogManagementGate_GETFragmentRoutes` — cashier
  gets 403 on all six routes; manager/admin/super_admin all get past the
  gate (never 403).
- `TestCatalogHandlers_CatalogManagementGate_GETFragmentRoutesNoSessionDenied`
  — no session at all is refused exactly like a cashier (fail-closed).

## Independent review

Performed directly in an isolated worktree
(`.claude/worktrees/agent-a370c2d5e6d19ef76`), checked out to
`origin/fix/2374-catalog-fragment-gate` (commit `0c26055`).

**Route coverage re-check (this review's own read, not trusting the
issue's stated list):** grepped every `mux.HandleFunc(` registration in
`handlers.go` (34 total) and classified each one:

- The six bare `GET /api/catalog/...` fragments this diff gates — now
  all start with `requireCatalogManagement`.
- Three full-page GET routes (`/catalog`, `/modifiers`,
  `/catalog/option-sets`) — already gated by `requireCatalogManagementPage`
  since #2357.
- Every bare-path (no-method-prefix) route (`/api/catalog/item`,
  `/api/catalog/item/update`, `/api/catalog/item/deactivate`,
  `/api/catalog/variant`, `/api/catalog/modifier-group`,
  `/api/catalog/modifier-option`, `/api/catalog/modifier-group/attach`,
  `/api/catalog/modifier-group/detach`, the `opt-out`/`opt-in` pair via
  the shared `optToggle` closure, `/api/catalog/item-station-routes`,
  `/api/catalog/variant/deactivate`, `/api/catalog/barcode`) rejects
  non-POST with `405 method not allowed` *before* the gate check, so
  there is no unauthenticated-GET path through any of them, and each is
  already gated on POST since #2312.
- Every explicit `POST /api/catalog/...` route is already gated
  (`/api/catalog/item-cost`, `/item-lead-time`, `/item-reorder-level`,
  `/option-set`, `/option-set-value`, `/item/option-sets`,
  `/item/generate-variants`, `/item/image`, `/item/icon`,
  `/variant/image`, `/barcode/delete`, `/barcode-backfill`).

No other GET route reads catalog data ungated. This fix, combined with
#2357 and #2312, closes the class completely for this file as of this
diff — nothing left to defer to a further follow-up card on this axis.

**TDD claim re-verified independently**, not taken on trust: reverted
just the six `if !requireCatalogManagement(w, r) { return }` insertions
in `handlers.go` (test file untouched), re-ran the two new tests. Both
failed with real assertion errors, not a compile error — e.g.:

```
catalog_management_gate_test.go:224: cashier GET /api/catalog/variant-options?item_id=itm1 = 200, want 403: {"data":[],"error":null}
catalog_management_gate_test.go:224: cashier GET /api/catalog/item-variants?item_id=itm1 = 200, want 403: ...
catalog_management_gate_test.go:224: cashier GET /api/catalog/item/icon-state?item_id=itm1 = 404, want 403: {"data":null,"error":"item not found"}
catalog_management_gate_test.go:224: cashier GET /api/catalog/barcode-backfill = 200, want 403: ...
catalog_management_gate_test.go:247: no-session GET /api/catalog/item-variants = 200, want 403: ...
```

Restored the six lines (`git checkout -- internal/pages/catalog/handlers.go`,
confirmed zero diff afterwards), re-ran the full gate-test file — all six
tests in `catalog_management_gate_test.go` pass
(`RealSessionGatesByRole`, `NoSessionDenied`, `PageRoutes`,
`GETFragmentRoutes`, `GETFragmentRoutesNoSessionDenied`,
`PageRoutesNoSessionDenied`).

**The two recurring bug classes this pipeline watches for** — a
file-write handler missing `os.MkdirAll`, and a cwd-relative path where
`paths.Data(...)` belongs — do not apply: confirmed the diff touches only
`internal/pages/catalog/handlers.go` and
`internal/pages/catalog/catalog_management_gate_test.go`, and neither
file in the diff performs any filesystem write or path construction at
all (pure in-memory permission-check insertions plus test assertions).

**No real client/shop name** appears anywhere in the diff — test fixture
identifiers are the generic `itm1`/`c1`/`u-manager`/etc. already used by
the sibling gate tests, and the one literal barcode
(`5449000000996`) is a standard example EAN-13, not a real shop's data.
No secret-shaped values anywhere in the diff.

**No UI/template surface touched**: `git diff --name-only` against the
diff's base shows only the two Go files above — no `web/`, no
`internal/pages/**/*.html`. The UX-guidelines checklist and "manual ships
with the feature" rule (`web/help/**`) genuinely do not apply; confirmed
directly rather than skipped on the task's say-so.

## Commands run (all in this worktree, on the checked-out fix branch)

- `gofmt -l .` — no output.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./internal/pages/catalog/...` — `ok` (6.17s).
- `golangci-lint run ./internal/pages/catalog/...` — `0 issues`.
- `bash scripts/ci/guard-data-access.sh` — clean (no SQL text outside
  `internal/data`/`internal/db`; this diff adds none regardless).
- `bash scripts/ci/guard-page-http-error.sh` — clean (this diff doesn't
  touch a page-route handler at all, so nothing to flag either way).
- `bash scripts/ci/guard-i18n.sh` — clean; confirmed a genuine no-op for
  this diff (no new locale-facing strings — the 403 response reuses the
  pre-existing `common.error.manager_or_admin_required` key every other
  `requireCatalogManagement` call site already uses).
- `bash scripts/ci/guard-compliance-claims.sh` — clean (343 files
  scanned, no forbidden claims).
- `go test ./...` (whole repo, plain, no `-race`) — every package `ok`
  (or `[no test files]`), `FULLTEST_EXIT:0`. Notable timings:
  `internal/data` 121.9s, `internal/pages` 219.7s, `internal/plugins`
  120.7s — all real work, no hang; `internal/pages/catalog` itself came
  back `(cached)` from the earlier targeted run.
- Revert → re-run → restore → re-run TDD cycle above, done live on this
  worktree's own checkout (never on a shared checkout).

## Safe to merge

**Yes.** The fix is minimal, mechanical, and exactly matches the
established precedent (#2312's `requireCatalogManagement` closure,
#2357's page-route sibling, and #2357's own review record naming this
precise gap as deferred follow-up work). Full gate is clean, the TDD
claim is independently re-verified with real revert/restore evidence,
route coverage was re-checked from scratch against every
`mux.HandleFunc` registration in the file rather than trusting the
issue's stated list, and neither of the two recurring bug classes nor
any client-data/UI-surface concern applies.

## Deferred

Nothing new. This diff closes the specific gap #2357 flagged and
deferred; the route-coverage re-check above found no further
`/api/catalog/*` GET route left ungated.
