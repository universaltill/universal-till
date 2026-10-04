# Review: Stock Locations node in my., till side (ut-docs#3383)

- Date: 2026-10-04
- Branch: `feat/3383-stock-locations-my`
- Author: Opus 5.5 (lane). Tester: session default. Reviewer: Fable 5.1 (independent,
  different model from both).
- Companions: ut-cloud `feat/3383-stock-locations-my` (directive types, snapshot field,
  `/ui/app/stores/{id}/stock-locations`), ut-my-shop `feat/3383-stock-locations-my` (the node).

## What shipped
- `internal/cloudsync/cloudsync.go`: three `Hooks` — `CreateStockLocation(name)`,
  `RenameStockLocation(id, name)`, `SetStockLocationActive(id, active)` — dispatched by `apply` for
  `create_stock_location` / `rename_stock_location` / `set_stock_location_active` exactly like
  `upsert_category`: nil hook → `"<type> is not supported on this till"`, payload shape checked in
  `apply` (`name`, `location_id`; `active` is required and read through `payload.optBool`, so an
  absent flag is never "deactivate"), the hook's error string is the directive's failure message.
- `internal/cloudsync/catalog_directives.go`: the three types are in `mainTillOnlyTypes` (the
  `stock_locations` table is primary-wins synced, `sync_admin_repo.go` `adminTables`) and in
  `catalogTypes`, so the snapshot re-pushes in the same tick.
- `pushSnapshotIfChanged`: additive `stock_locations: [{id, name, is_active}]` from
  `POSRepo.ListStockLocationsForAdmin` (inactive included, retire-mangle stripped), inside the
  hashed payload so a locations-only change pushes; a read error leaves the field out rather than
  sending `[]`.
- `internal/pages/cloudsync_wire.go`: `cloudCreateStockLocation`, `cloudRenameStockLocation`,
  `cloudSetStockLocationActive`, wired in `buildCloudHooks`. Same `POSRepo` calls and guards as
  `locations_page.go`'s three POST handlers (`StockLocationInUse`, `CountActiveStockLocations <= 1`),
  `requirePrimaryDirective` before every write, `auditCloudDirective` under `"system"` with the
  page's own action names. Refusals are `httpx.T("en", "locations.error.*")` — the page's wording.
- `web/help/img/manifest.json`: surface hash only (escape hatch; no rendered pixel changes).
- Tests: `cloudsync/stock_locations_3383_test.go` (dispatch, payload validation, bool/string
  `active`, refusal text verbatim, type-set membership, snapshot carries locations and re-pushes on
  a locations-only change), `pages/cloudsync_stock_locations_3383_test.go` (create once + audit,
  idempotent retry, rename + no-op rename not audited, in-use and last-location refusals in the
  page's words with no write and no audit row, deactivate/reactivate, satellite refused, hooks
  wired).

## Independent checks (Fable)
- **Pattern.** Mirrors `cloudUpsertCategory` line for line: dedupe scan before the primary gate
  (a replayed directive is a no-op on a satellite too), audit after the write, English result
  message. `findStockLocation` / idempotency compare against `ListStockLocationsForAdmin`, the same
  read the page uses.
- **Main-till gating** is double: `Tick` serves the types only on the main till
  (`mainTillOnlyTypes`) and every hook calls `requirePrimaryDirective` before writing (tested on a
  satellite with `sync.primary_url` set).
- **Idempotency** read, not taken on trust: create of an existing active name (case-insensitive)
  returns `"… already exists"` with no insert and no audit row; rename to the current name and
  set-active to the current state return `"… unchanged"` / `"… already (in)active"` before the
  primary gate and before any write.
- **Refusal text path.** `apply` returns `("failed", err.Error())`; the cloud stores it as the
  directive `result`; my.'s tracker maps `result` → `reason` on `failed`; `failureText` renders
  "The till refused this change: <text>". `TestCloudSetStockLocationActive_RefusesInUseWithLocationsPageText`
  pins the text to `locations.error.in_use`.
- **Recurring bugs:** no file write anywhere in the diff (no `os.MkdirAll` / `paths.Data`
  concern); no cwd-relative path.
- **Help.** The till's own `/locations` page and templates are untouched (`web/ui` has no diff);
  only the cloudsync wiring changed, so no `web/help/` topic changes. `guard-docs-shots.sh` passes
  on the refreshed surface hash.
- **No real client/shop names; no secret literals** (`loc_main`, `Back room`, `Cellar`,
  `http://primary.example`).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | **medium** | **A retired location's name was treated as free, but `stock_locations.name` is `UNIQUE` (`001_init.sql:70`, case-sensitive) and deactivation keeps the name.** `cloudCreateStockLocation` only deduped against *active* rows; the cloud endpoint and my. encoded the same rule ("a retired location's name may be used again (the till allows it)" — the UI test's own comment). Proven on the branch with a new test: create `"back room"` over a retired `"Back room"` **inserted a second, near-duplicate row** (case differs, so the index let it through); create of the exact name would have failed the directive with the raw `UNIQUE constraint failed` SQL text, not the page's wording. Rename had the same gap. | **Fixed** by the reviewer: `stockLocationNamed` scans every row (any state, case-insensitive); a create over an active name stays a no-op success, over a retired name refuses with `locations.error.create` ("Could not create location (name already used?)" — what an operator sees at the till for the same duplicate); a rename to any other location's name refuses with `locations.error.rename`; a case-only rename of a location's own name is still a real rename. `findStockLocation` now takes the already-loaded list (one read per hook). Tests `TestCloudCreateStockLocation_RefusesRetiredNameInPageWords` and `TestCloudRenameStockLocation_RefusesTakenName` (both fail on the pre-review snapshot: `err = <nil>`). Same rule applied in ut-cloud and ut-my-shop, see their records. |
| 2 | info | `cloudsync_wire.go` is a non-test `internal/pages` file, so each edit bumps the docs-shots surface hash. Both the Dev's and the reviewer's bumps are escape-hatch refreshes (`update-docs-shots-surface-hash.sh`): no `web/ui` template or screenshotted route changed. | Accepted; `Docs-Shots-Unchanged: true` trailer on the commit. |
| 3 | info | The cloud's `DirectiveMinTillVersion` has no entry for the three types, like `create_item` / `upsert_category`: an older till answers "unknown directive type" and my. shows that. Consistent with precedent. | Accepted. |

Nothing blocking after finding 1.

## Verification
- **TDD, re-verified by the reviewer** inline (mutate → run → `git checkout --` restore → re-run,
  within one turn):
  - `cloudSetStockLocationActive` in-use guard disabled (`if inUse && false`):
    `TestCloudSetStockLocationActive_RefusesInUseWithLocationsPageText` fails ("deactivating a
    location holding stock must be refused") and `TestBuildCloudHooks_WiresStockLocations` fails
    ("wired deactivate must keep the in-use guard"). Restored: `ok`.
  - Finding 1's two new tests run against the pre-fix hooks: both fail with `err = <nil>`; after the
    fix: pass.
- **Gate (reviewer's own run, after the fix):** `gofmt -l .` empty, `go build ./...`,
  `go vet ./...`, `go test -count=1 ./internal/cloudsync/... ./internal/pages/...` all green;
  `scripts/ci/guard-docs-shots.sh` green on the refreshed manifest.
- **Not run:** a real till against the deployed cloud; `TestSnapshotIncludesStockLocations` drives
  `Tick` against the in-process fake cloud and the new pages tests use the real SQLite seed.

## Deferred
- None in this repo. (UX follow-up on the my. side: when a name clashes with a *retired* location
  the message could point at Reactivate — see the ut-my-shop record.)

## Verdict
**Safe to merge** with the reviewer's fix applied. Part of ut-docs#3383; Done once the ut-cloud and
ut-my-shop companions are merged and DevOps has confirmed the deploy.
