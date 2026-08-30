# Code review: `fiscal_register_de` moves into plugin-owned KV storage (ut-docs#1106)

- **Card**: ut-docs#1106 — move Germany's §146a Abs. 4 AO till/TSE
  notification-bookkeeping out of core into `ut-plugin-tax-de`, per
  [ADR-0050](https://github.com/universaltill/ut-docs/blob/main/adr/0050-legal-obligation-boundary-country-plugin-vs-core.md)
  Decision 1.
- **Design**: [ADR-0072](https://github.com/universaltill/ut-docs/blob/main/adr/0072-fiscal-register-de-moves-into-plugin-storage.md)
  (written this cycle, amended post-review — see below).
- **Build**: `fable` subagent (complexity:hard). **Review**: `opus` subagent,
  isolated worktree, independent from the model that wrote the code.
- **Branch**: `fix/1106-fiscal-register-de-plugin-storage`.

## What shipped

- `internal/data/plugin_repo.go`: two new generic KV primitives —
  `ListStorageByPrefix` (enumeration by key prefix, LIKE-escaped) and
  `DeleteStorageKey` (single-key delete) — plus, post-review,
  `DeleteStorageExceptPrefix` (see Finding B1 below).
- `internal/data/fiscal_repo.go`: removes the three table-backed
  `fiscal_register_de` methods (`CreateFiscalRegisterDE`/`ListFiscalRegisterDE`/
  `DecommissionFiscalRegisterDE`); adds `ListRegisterLocations` (register+location
  join, no `is_active` filter — history must show a decommissioned till's
  register name) and `FiscalRegisterDEStore` (Create/List/Decommission backed by
  `plugin_storage`, namespaced under `com.universaltill.tax-de`,
  `FiscalRegisterDEKeyPrefix = "fiscal_register:"`). `SetStockLocationAddressDE`
  is untouched.
- `internal/db/migrations/075_fiscal_register_de_to_plugin_storage.sql`: new,
  append-only migration — copies every existing row into `plugin_storage` as a
  JSON blob (`json_object(...)`, keys verified to match
  `fiscalRegisterDERecord`'s json tags exactly), then `DROP TABLE
  fiscal_register_de`. Includes a `CREATE TABLE IF NOT EXISTS` shell needed only
  for a test harness that replays migrations from a rewound `schema_migrations`
  range without 059 — inert on every real till (059 always creates the real
  table first).
- `internal/pages/fiscal_register_page.go`: three call sites swapped to the new
  store. Auth, validation, grouping, audit, i18n keys, HTTP contract, and
  rendered output are all unchanged — confirmed by `guard-docs-shots.sh` after
  a full screenshot regen (fiscal-register's own screenshots are byte-identical
  across all 4 locales; only the manifest hash needed updating because the file
  itself changed).

## Independent review — findings and disposition

One **blocker**, two **should-fix**, six **note** (not fixed, judged
non-blocking or genuinely out of scope). Full transcript retained in the
pipeline session; summarized here.

### B1 (blocker) — fixed pre-merge
Automatic, non-operator plugin-lifecycle paths — a joined till's sync tick
auto-pruning a plugin the primary no longer has
(`internal/pages/sync_admin.go`), and a pinned-version-mismatch rollback
(`internal/pages/cloudsync_wire.go`) — both funnel through
`plugins.UninstallPlugin`, whose blanket `DeleteStorage(pluginID)` would have
permanently destroyed a shop's real AO bookkeeping with zero operator action
or warning. Before this diff the data was immune to any plugin-lifecycle
event (a core table); the original ADR-0072 design would have made a
legally-relevant record destroyable by a sync hiccup.

**Fix**: new `PluginRepo.DeleteStorageExceptPrefix`; `UninstallPlugin` now
preserves everything under `fiscal_register:` unconditionally, for every
removal path — deliberate or automatic. This drops #1106/#1026's original
"uninstalling removes the surface" acceptance criterion in favor of
ADR-0042's already-accepted "destroys nothing" principle — ADR-0072 amended
to record the reversal and why. New test:
`TestUninstallPlugin_PreservesFiscalRegisterStorage` (pins that
`fiscal_register:*` survives uninstall while an unrelated key, e.g.
`tse_result:*`, is still cleared as before) plus
`TestPluginRepo_DeleteStorageExceptPrefix` (preserved-prefix semantics +
LIKE-wildcard-escaping, sibling-plugin isolation).

### S2 (should-fix) — not fixed here, re-scoped onto its existing follow-up card
The 1024-key-per-plugin `plugin_storage` cap is shared with `tse_result:*`
(unbounded, one key per sale, no pruning — already tracked as ut-docs#1299,
filed while writing ADR-0072). Review found the cap, once hit, also blocks
*this* page's `Create`/`Decommission` (both call `StorageSet`), which #1299
didn't originally scope. Added as a comment on ut-docs#1299 rather than
reopening the cap question in this PR — the real fix (raise the cap, or prune
`tse_result:*`) is a separate design decision.

### S3 (should-fix) — fixed pre-merge
`plugin_storage` under a plugin's own id is writable by that plugin's own
WASM guest code (`hostStorageSet`), which `ut-plugin-tax-de` could reach for
the `fiscal_register:` namespace too — a new trust surface the old core-only
table didn't have. `List` previously aborted the entire page on one
unparseable entry. **Fix**: skip-and-log instead (mirrors
`export_repo.go`'s existing unparseable-`content_json` precedent). New test:
`TestFiscalRegisterDE_ListSkipsMalformedEntry`.

### Notes (not fixed, judged non-blocking)
- N1 — the migration's `CREATE TABLE IF NOT EXISTS` shell is verified safe
  (SQLite's `IF NOT EXISTS` never compares/alters an existing table); flagged
  as test-harness DDL living in production SQL, a `rewindFiscalRegisterDE075`
  test helper (matching this file's own `rewindPaymentsVoucherID072`/
  `rewindCountryDefaultLocale073` neighbors) would be cleaner but isn't a
  correctness issue.
- N2 — `INSERT` could be `ON CONFLICT DO NOTHING` for extra safety; unreachable
  today since the shell path is always empty on replay.
- N3 — migrated rows carry RFC3339 `updated_at` into `plugin_storage.updated_at`
  where other writers use `datetime('now')` format; nothing reads/sorts that
  column, confirmed inert.
- N4 — no client-side `maxlength` on the create form; a realistic record is
  ~350 bytes against a 64KiB cap, theoretical.
- N5 — `FiscalRegisterDEStore` keeps its own `*sql.DB` for one existence
  check; a `POSRepo.RegisterExists` method would be marginally cleaner.
- N6 — the migration round-trip test doesn't separately assert
  `plugin_storage.updated_at` was copied (it does assert the JSON field,
  which is the one actually read).

## Verified beyond automated tests

- **TDD re-verified independently by the reviewer**, not taken on the
  implementer's word: production code (migration + all three touched `.go`
  files) reverted to `main`'s versions in the isolated worktree, confirmed
  the relevant tests fail for the right reason (compile errors on the new
  symbols in `internal/data`; logical 500s/`sql: no rows` in
  `internal/pages` because the fixture now provides `plugin_storage`, not the
  dropped table), then restored and confirmed green again. Separately
  verified that deleting only the migration's `IF NOT EXISTS` shell breaks
  exactly the two upgrade-replay tests its own header comment claims it
  protects, and nothing else.
- **Round-trip fidelity checked line-by-line**: all 13 columns/fields present
  in migration 059's table ↔ migration 075's `json_object(...)` keys ↔
  `fiscalRegisterDERecord`'s json tags — no field dropped, added, or
  misspelled; NULL `commissioned_on`/`decommissioned_on` confirmed to survive
  as JSON `null` → Go `nil`, exercised by a real migration replay
  (`TestFiscalRegisterDE_Migration075RoundTrip`, not hand-constructed
  `plugin_storage` rows).
- **List ordering equivalence** (SQL `CASE WHEN loc.name IS NULL...,
  loc.name, reg.name, acquired_on` → Go `sort.SliceStable`) checked against
  the schema (`stock_locations.name TEXT NOT NULL UNIQUE`, so "NULL name" and
  "no joined location" are provably the same condition — the divergence case
  the reviewer set out to find is schema-impossible).
- **Register-existence check replacing the dropped FK** confirmed
  behaviorally identical: `foreign_keys = ON` is genuinely set in production,
  the old FK had no `is_active` predicate, neither does the new
  `SELECT COUNT(*) FROM registers WHERE id = ?` check.
- No real client/shop name in test/seed data (`Main Shop`, `Front Till`,
  fictitious German addresses). No secret-shaped literals.
- No UI-visible change: confirmed by a full `make docs-shots` regen —
  `fiscal-register`'s screenshots (all 4 locales) are byte-identical; only
  `web/help/img/manifest.json`'s hash needed updating because
  `fiscal_register_page.go` itself changed. Three unrelated screenshots
  (`sell` en/tr, `translations` fa) picked up a few-dozen-byte diff from
  normal rendering nondeterminism (font/anti-aliasing jitter), not a content
  regression — no template, CSS, or JS under those topics was touched by this
  diff.
- Manual (`web/help/en/fiscal-register.md`): checked against the review's
  finding — its "none of them are ever deleted" claim, which the review
  initially flagged as newly-false, is restored to true by the B1 fix (the
  claim was always about decommission-is-update-not-delete; it now also holds
  across plugin uninstall). No manual edit needed.

## Gate (re-run after every fix, not just once)

```
gofmt -l .                                              # empty
go build ./...                                          # ok
go vet ./...                                             # ok
go test -count=1 ./internal/data/... ./internal/pages/... \
  ./internal/db/... ./internal/plugins/...               # all ok
scripts/ci/guard-data-access.sh                          # ✓
scripts/ci/guard-kiosk-engine.sh                         # ✓ (unaffected, checked anyway)
scripts/ci/guard-plugin-menu-read.sh                     # ✓ (unaffected, checked anyway)
scripts/ci/guard-i18n.sh                                 # ✓
scripts/ci/guard-compliance-claims.sh                    # ✓
scripts/ci/guard-help-topics.sh                          # ✓
scripts/ci/guard-migration-version-collision.sh          # ✓
scripts/ci/guard-docs-shots.sh                           # ✓ (after make docs-shots)
```

## Verdict

**Safe to merge.** The one blocker (B1) is fixed and pinned by a new test;
the two should-fix findings are either fixed (S3) or correctly re-scoped onto
an existing, more appropriate follow-up card (S2, ut-docs#1299) rather than
expanding this PR's scope. All six notes are genuinely non-blocking.

## Deferred, not this card

- ut-docs#1299 — `tse_result:*` unbounded storage growth (now also blocks
  this page's writes once the shared cap is hit; comment added there).
- N1/N5/N6 above — small, non-blocking cleanups, left for whoever next
  touches this file rather than padding this diff.
