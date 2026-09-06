# Schema-drift guard for `adminTables` (ut-docs#1586)

## What shipped

`internal/data/sync_admin_repo.go` gained a `nonAdminTables map[string]string`
classifying every table in the schema that is **not** in `adminTables` (the
LAN admin-sync bundle, ADR-0011), one reason each. `internal/data/schema_drift_test.go`
adds `TestSchemaTablesAreClassified`, which opens a really-migrated DB, reads
`sqlite_master`, and fails if any real table is in neither list, or if either
list names something that isn't a real table.

This closes the gap ut-docs#1546 found by hand (`tables`/`kitchen_stations`
sat unsynced for a long time with nothing to catch it) — it is now a CI
failure, not a manual audit, the moment a migration adds an unclassified
table.

Scope, deliberately: **no behavior change to sync itself.** Every table whose
classification was genuinely uncertain was excluded (not added to
`adminTables`) with an honest "flagged, not decided" reason and its own
follow-up card — mirroring how ut-docs#1554 split off #1584/#1585/#1589
rather than deciding everything in one PR. Six follow-ups came out of this
card: ut-docs#1667 (item_modifier_groups/options — a real gap, no
`requirePrimary` gate either), #1668 (vouchers — mutable cross-till balance),
#1669 (country_settings — admin-editable jurisdiction defaults), #1670
(plugin_storage / German fiscal register — found in review, see below), #1671
(price_history — found in review), #1672 (table occupancy across tills —
found in review).

## Independent review

Opus, fresh context, isolated worktree (`isolation: "worktree"`, per the
ut-docs#386 mitigation — the revert-then-restore TDD verification below never
touched this session's own checkout).

**Verdict: not yet — merge after two comment-only fixes. No behavior
defects; the guard mechanism itself was correct and well-built.**

Commands the reviewer actually ran: `go build ./...`, `go vet ./...`,
`gofmt -l` on both changed files, `go test ./internal/data/... -run
TestSchemaTablesAreClassified -v` (PASS), the full `go test
./internal/data/...` package (ok, 37.9s), `golangci-lint run
./internal/data/...` (0 issues).

**TDD claim independently re-verified (not just eyeballed):** the reviewer
commented out `tables` and `kitchen_stations` from `adminTables` and added a
bogus table name to `nonAdminTables`, re-ran the test, and confirmed it
failed naming exactly those two tables plus the bogus entry — i.e. the guard
would genuinely have caught the original #1546 bug. Then reverted and
confirmed `git status`/`git diff` were clean. Separately re-verified the
reverse-drift case (a real table named in both lists) fails correctly.

**Independent coverage reconciliation** (the reviewer's own script, not a
re-run of my test): 26 `adminTables` + 52 `nonAdminTables` = 78, matching the
78 real tables (77 `CREATE TABLE` statements across the migrations, minus one
grep false-positive — the word "time" inside a comment on `001_init.sql:16`,
not a real table — plus `schema_migrations`, created by the Go migration
runner rather than a migration file). Zero overlap, zero stale entries, zero
unclassified.

### Findings — both fixed

1. **`price_history`'s original reason was factually wrong.** It claimed the
   table was pure append-only history and inert to checkout. Neither is true:
   `AppendPriceHistoryItem`/`Variant` (`pos_repo.go:7136,7158`) UPDATE the
   prior row's `ends_at`, a bulk item delete (`pos_repo.go:5572`) DELETEs
   rows, and `ResolveCurrentPrice` (`pos_repo.go:6866`) consults an open
   `price_history` row **before** falling back to `items`' synced price — an
   open row overrides the synced price, not the reverse. Currently latent
   (nothing in production writes this table today; the only caller,
   `internal/pos/pricing.go`, has no production entry point), but a real risk
   the moment a scheduled-price-change feature ships. Fixed: the reason now
   states the real behavior and the table moved into the "genuinely open
   question" group with a follow-up (ut-docs#1671) rather than asserting a
   settled, wrong verdict.

2. **`plugin_storage`'s "till-local by design" reason was wrong for a real,
   shipped consumer.** ADR-0072/ut-docs#1106 repurposed its `fiscal_register:`
   key prefix as the backing store for Germany's §146a Abs. 4 AO till/TSE
   register (`FiscalRegisterDEStore`) — shop-wide, compliance-relevant data —
   and `/fiscal-register`'s mutation handlers have no `requirePrimary` gate,
   the second table found with the exact #1546 shape. Fixed: the reason
   corrected to describe the real risk and split into its own follow-up
   (ut-docs#1670) rather than asserting the table is safely per-till.

### Should-fix — also addressed

3. **`held_sales`'s reason understated a real consequence.** Excluding it
   from sync is still correct (a primary-wins bundle with `deleteMissing`
   would erase a satellite's own parked orders — the strongest argument in
   the file), but combined with `tables` now syncing (#1546) and
   `table_claims` also correctly excluded (ephemeral, cleared at boot), the
   shop's floor plan is shared across tills while table *occupancy* is not —
   a table held on one till can read FREE on another. Fixed: the reason now
   names this consequence explicitly, with a follow-up (ut-docs#1672) since
   the fix needs a different mechanism than this bundle, not a re-scoping of
   `held_sales`/`table_claims`.

### Verified-correct on independent spot check (10 entries, not re-trusted)

`table_claims` (cleared at boot, `ClearAllTableClaims`), `audit_log`
(`PerTillSettingPrefixes` location confirmed), `fiscal_sign_starts` /
`fiscal_tse_signatures` (1:1 on `sale_id`, confirmed), `plugin_install_status`
(confirmed `SyncPluginsRepo`'s own source table), `inventory` /
`stock_movements` (D3's separate sync stream confirmed to exist),
`sync_journal_quarantine`, `schema_migrations` (correctly attributed to the
Go migration runner, not a migration file), `schema_lineage`,
`country_settings` (correctly flagged as an open question — really does
drive retention via `archive_min_days`), `item_modifier_groups`/`options`
(missing `requirePrimary` gate independently confirmed).

**FK safety:** the diff is purely additive after the existing `adminTables`
close — that var and its FK-ordering comments are untouched. `nonAdminTables`
is a map and drives no insert/delete order, so it cannot disturb them.

Steps not applicable to this diff, confirmed rather than skipped silently: no
file writes (no `os.MkdirAll` risk), no path handling (no `paths.Data(...)`
risk), backend-only (no UI/help-topic surface to check), no client/shop names
or secret-shaped literals.

## Verified beyond automated tests

- `gofmt -l` clean on both changed files.
- `go build ./...`, `go vet ./...` clean (repo-wide).
- `go test ./...` — full repo suite green (see cycle's own run).
- `golangci-lint run ./...` — 0 issues (repo-wide).
- `scripts/ci/guard-data-access.sh` — passes (the new test's raw
  `sqlite_master` query lives in `internal/data`, the one place that's
  allowed).

## Safe-to-merge

Yes, after the three comment fixes above (all landed in this same PR, verified
against the corrected file with a fresh full-package test run and lint pass).

## Explicitly deferred (by design, not oversight)

Six follow-up cards, none of which change sync behavior in this PR:
ut-docs#1667, #1668, #1669, #1670, #1671, #1672 — see "What shipped" above for
which table(s) each covers and why.
