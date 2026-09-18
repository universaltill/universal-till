# internal/pages: clone the migrated template DB in the remaining ad-hoc `db.Open` sites

**Card:** universaltill/ut-docs#2385 (also unblocks universaltill/ut-docs#2191)
**PR:** universal-till fix/2385-pages-adhoc-db-open-remaining-sites
**Model routing:** `complexity:medium` — Dev via Sonnet subagent (isolated worktree), independent review via Opus subagent (isolated worktree).

## What shipped

Follow-up to ut-docs#2191/#2219, and to the PR documented in
`2026-09-18-pages-adhoc-db-open-template-clone-2191.md` (the 6
heaviest remaining ad-hoc sites, 33 of ~74). This PR converts the rest:
37 files, 38 more ad-hoc `db.Open(filepath.Join(t.TempDir(), "…"))` /
`appdb.Open(...)` call sites in `internal/pages` (package `pages`, not
its `catalog`/`common` subpackages) to call the existing shared helper
`openPagesTestDB(t)` instead, same pattern as before — no new helper
introduced, no production code touched.

| file | sites converted |
|---|---|
| `inventory_category_filter_2119_test.go` | 2 |
| `hold_cross_till_test.go` | 2 |
| `held_sale_sync_proxy_test.go` | 2 |
| `voucher_api_test.go` | 1 |
| `translations_page_test.go` | 1 |
| `tables_page_test.go` | 1 |
| `table_picker_api_test.go` | 1 |
| `sync_vouchers_test.go` | 1 |
| `sync_tables_test.go` | 1 |
| `sync_tables_claim_test.go` | 1 |
| `sync_sales_payment_method_fk_test.go` | 1 |
| `sync_orders_test.go` | 1 |
| `sync_held_sales_test.go` | 1 |
| `sync_admin_test.go` | 1 |
| `suggestions_api_test.go` | 1 |
| `setup_restore_import_test.go` | 1 |
| `self_order_shop_test.go` | 1 |
| `product_reports_test.go` | 1 |
| `pos_scan_barcode_test.go` | 1 |
| `pos_modifiers_variants_test.go` | 1 |
| `pos_modifiers_api_test.go` | 1 |
| `plugin_api_test.go` | 1 |
| `pairing_join_test.go` | 1 |
| `pairing_api_test.go` | 1 |
| `order_tracking_test.go` | 1 |
| `order_status_test.go` | 1 |
| `kitchen_stations_page_test.go` | 1 |
| `kitchen_display_test.go` | 1 |
| `kiosk_counter_orders_page_test.go` | 1 |
| `invoice_test.go` | 1 |
| `import_page_test.go` | 1 |
| `eod_tax_bands_test.go` | 1 |
| `designer_page_test.go` | 1 |
| `data_api_test.go` | 1 |
| `country_settings_page_test.go` | 1 |
| `cloudsync_wire_test.go` | 1 |
| `bluetooth_devices_page_test.go` | 1 |
| **total (37 files)** | **38 sites** |

For sites whose helper's return value is consumed by other files via
`.DB` field access, the `*db.DB` wrapper (`&db.DB{DB: openPagesTestDB(t)}`)
was kept so those unmodified files keep compiling unchanged; everywhere
else the return value was collapsed straight to `*sql.DB`, per the card's
stated preference. `internal/db.DB` is `type DB struct { *sql.DB }` with
only unexported methods, so the two forms are behaviourally identical.

## Scope: 4 files deliberately excluded, and confirmed untouched

- `backup_api_test.go` and `data_backup_manager_gate_test.go`'s
  `TestBackupEndpoints_RealSessionGatesByRole` — both need a real, known
  on-disk `Cfg.DBPath` for `db.Snapshot`/`BackupDir`-style backup/restore
  functionality; `openPagesTestDB` deliberately hides its path.
- `sync_api_test.go`'s `newSyncDepsWithPath` — builds `cfg.DBPath`
  consumed by `appdb.PendingRestore(path)`/`appdb.ReplicaIdentityPath(path)`
  and a backups dir; same reason.
- `import_page_querycount_test.go` — opens a **second**, independent raw
  connection to the same on-disk file via a custom counting
  `driver.Connector`, which needs the literal path.  The independent
  review also noted this one is doubly correct to exclude:
  `openPagesTestDB`'s `journal_mode=MEMORY` is unsafe with multiple
  connections to the same file, so converting it risked a genuinely wrong
  result, not just a compile error.

`git diff` against these 4 paths is empty — confirmed by both the Dev
pass and the independent review.

## Independent review (Opus, isolated worktree)

Verdict: **safe to merge as-is, no blocking findings.** Full gate
reproduced from scratch (not trusting Dev's own numbers):

- `gofmt -l internal/pages/` — clean.
- `go build ./...` / `go vet ./...` — clean.
- `golangci-lint run ./internal/pages/...` — `0 issues.`
- `guard-data-access.sh` / `guard-i18n.sh` — both pass.
- `go test ./internal/pages/... -count=1` — green, **77s** (vs. 185-187s
  baseline before this + the prior PR, ~2.4x faster).
- **`go test ./internal/pages/... -race -count=1 -timeout 25m` — green,
  927s (~15m27s), no `WARNING: DATA RACE`, no timeout.** The prior PR's
  own review explicitly recorded this NOT completing within budget — it
  now does. This is the evidence ut-docs#2191 needs to close.

Correctness verified, not assumed:
- `internal/db.DB`'s wrapper-identity claim confirmed by reading its
  source (all methods unexported).
- Template clone (`buildRealDBTemplate`) is a plain `db.Open` + WAL
  checkpoint, no extra seeding — byte-equivalent to what each site's own
  `db.Open` produced.
- Every two-DB-handle site (`held_sale_sync_proxy_test.go`,
  `hold_cross_till_test.go`) calls `openPagesTestDB(t)` twice
  independently — no accidental handle sharing between primary/replica.
- PRAGMA differences (`journal_mode=MEMORY`, `synchronous=OFF`,
  `MaxOpenConns=1`) checked against every converted file for anything
  that could plausibly depend on crash-durability or true multi-connection
  concurrency — nothing does; the one file with goroutines
  (`sync_admin_test.go`) doesn't touch the DB from them.
- Close discipline preserved everywhere (`t.Cleanup`/`defer` kept as-is).
- No `os.MkdirAll`/cwd-relative-path issue (diff only removes path
  construction, adds none). No real client/shop name, no secret-shaped
  literal.

**One finding fixed before merge:** `sync_admin_test.go`'s doc comment
read "...REAL, fully migrated database ... rather than
openPagesTestDB+seedForPages" directly above a function that (after this
diff) calls `openPagesTestDB` on the very next line — self-contradictory.
Fixed to "...rather than seedForPages" (the actual, still-true
distinction — the simplified fixture schema vs. a real migrated one).

**Two non-blocking notes, deliberately left as-is** (judgement calls, not
defects — noted here so they aren't rediscovered as new findings later):
- `data_api_test.go:654-655` and `pairing_api_test.go:465-466` carry a
  milder stale parenthetical ("...REAL migrated database (internal/db.Open)")
  — still substantially true since `openPagesTestDB` goes through
  `internal/db.Open` on the clone, just once-removed.
- `eod_tax_bands_test.go`'s `etbOpenDB` and `sync_admin_test.go`'s
  `newMigratedSyncDeps` both keep a `name string` parameter that no
  longer affects behaviour (the distinct filenames it used to produce are
  now hidden inside `openPagesTestDB`). Compiles clean, `golangci-lint`
  doesn't flag it (`unparam`/`revive` aren't enabled). Left for a future
  pass rather than touched here, to keep this diff to what the card
  scoped.

## What was verified beyond automated tests

Coverage completeness: `grep -rn "appdb\.Open(\|db\.Open(" internal/pages/*_test.go`
returns only the 4 excluded files plus the two helper definitions
themselves — the 37 converted + 4 excluded files account for exactly the
41 files the card named. Nothing silently skipped.

## Safe-to-merge verdict

Yes. Independent Opus review found no blocking issues (one doc-comment
fix applied); full gate green including the previously-blocked full-package
`-race` run; git identity re-verified before commit.

## Explicitly deferred

- The two non-blocking notes above (stale-but-true parentheticals, dead
  `name` parameters) — left for a future pass, not urgent.
- Whether to actually close ut-docs#2191 is Scrum Master's own follow-up
  once this merges — this PR provides the evidence (full-package `-race`
  now completes) but doesn't itself change `internal/db` or the CI
  workflow's `-race` usage.
