# Review: fiscal-register primary gate + scoped `plugin_storage` sync (ut-docs#1670)

**Card:** ut-docs#1670 — `plugin_storage` backs the shop-wide German
fiscal-register (TSE) record with no `requirePrimary` gate and no sync,
the same shape as ut-docs#1546 (shop-wide state missing from the LAN
admin-sync bundle, with no primary-only mutation gate, causing silent
divergence between a shop's primary till and its satellite tills).

**Complexity:** medium. **Built:** Sonnet (inline). **Reviewed:** Opus
(independent subagent, isolated worktree — `isolation: "worktree"`,
per this card's model routing).

## What shipped

1. `internal/pages/fiscal_register_page.go` — a `requirePrimary` gate
   (mirrors `registers_page.go`'s exact pattern) on all three mutating
   handlers: `POST /api/fiscal-register` (create), `POST
   /api/fiscal-register/{id}/decommission`, and `POST
   /api/fiscal-register/locations/{id}/address`. A replica now refuses
   with a localized redirect instead of accepting a write that would
   silently revert (or, before this fix, never even reach the primary at
   all) on the next admin pull.
2. `internal/data/sync_admin_repo.go` — `plugin_storage` joins
   `adminTables`, scoped to **both** `plugin_id ==
   FiscalRegisterDEPluginID` **and** `key` prefixed
   `FiscalRegisterDEKeyPrefix` ("fiscal_register:"). `DumpAdmin` filters
   on both dimensions; `ApplyAdmin` exempts the table from the generic
   `deleteMissing`/`upsertRow` path (the bundle is deliberately a small
   subset of the table) and dispatches to a new
   `applyFiscalRegisterStorage`, a scoped delete-then-insert modeled
   directly on the existing `applyPluginSettings`. Removed from
   `nonAdminTables` (a table can't be classified in both).
3. `internal/data/fiscal_repo.go` — new exported
   `FiscalRegisterDEPluginID` constant, co-located with the existing
   `FiscalRegisterDEKeyPrefix`; `internal/pages/import_page.go`'s
   `taxDePluginID` now aliases it instead of holding an independent
   literal copy of the same plugin id.
4. New locale key `fiscalregister.error.replica_use_primary` in
   `web/locales/{en,fa,tr,ar}.json`, wording mirrors the existing
   `registers.error.replica_use_primary`.
5. `web/help/{en,fa,tr,ar}/fiscal-register.md` — a new bullet documenting
   primary-only editing, mirroring `multitill.md`'s established "main
   till"/"joined till" wording. `make docs-shots` re-run (required by
   `guard-docs-shots.sh` since the app-surface hash changed, even though
   the page renders identically — confirmed the fiscal-register
   screenshots themselves are byte-identical across all 4 locales; only
   an unrelated, already-known non-deterministic PNG re-encode on
   `sell.png` moved, pre-existing pipeline flake, not introduced here).
6. `ut-docs/architecture/lan-sync.md` — a new bullet under Increment D2
   documenting this mechanism, mirroring the existing "Shared plugin
   settings" entry, per the standing "behaviour changes update the
   affected doc in the same session" rule.
7. Tests: `TestAdminDumpApplyRoundTrip_FiscalRegisterStorage`
   (`internal/data`) and `TestFiscalRegisterPage_MutationsRefusedOnReplica`
   + `TestFiscalRegisterPage_NonManagerOnReplicaGetsManagerError`
   (`internal/pages`).

## Independent review — findings and resolution

The Opus review ran independently in an isolated worktree
(`isolation: "worktree"`, off a `WIP: pre-review snapshot` commit), built
and tested the diff itself, and **independently re-ran both TDD mutation
claims** (temporarily removed the `requirePrimary` call from the create
handler, and separately widened the scoped `DELETE` to the whole table)
— both times confirming the corresponding new test failed with a real
assertion failure, then passed again after restoring. Overall verdict:
**safe to merge**, with one finding recommended (not required) before
merge and several non-blocking notes.

### Fixed before merge

- **N1 (real, empirically confirmed) — key-prefix-only scoping left a
  cross-plugin gap.** The first draft scoped the sync by `key` prefix
  alone, reasoning that `internal/data` doesn't own the DE tax plugin's
  id constant (per `FiscalRegisterDEStore`'s own doc comment: "the
  caller passes the plugin id... rather than this package redefining the
  constant"). The reviewer built a throwaway test proving this was a
  real, **bidirectional** leak: a different, unrelated plugin choosing a
  `plugin_storage` key that happens to share the literal
  `fiscal_register:` prefix would have that row **deleted** on a
  satellite (collateral damage from the scoped `DELETE`) and
  **broadcast** in the primary's dump — even though every other
  `plugin_storage` accessor in this codebase (`StorageGet`/`StorageSet`/
  `ListStorageByPrefix`/`DeleteStorageKey`/`DeleteStorage`, and
  `DeleteStorageExceptPrefix` most tellingly, added by the ADR-0072
  review specifically to protect this same prefix) scopes by `plugin_id`.
  **Fix:** added `FiscalRegisterDEPluginID` to `internal/data` (next to
  `FiscalRegisterDEKeyPrefix`, the constant it pairs with), scoped both
  the dump filter and `applyFiscalRegisterStorage`'s delete+insert by
  `plugin_id` as well as key prefix, and had `internal/pages`'
  `taxDePluginID` alias the new constant instead of holding an
  independent literal — one source of truth instead of two. The
  regression test was extended with a same-prefix-different-plugin_id
  row and re-verified real via the same revert/restore mutation method
  the reviewer used for the original claims (confirmed failing pre-fix,
  passing post-fix).
- **N3 (documentation) — a misleading comment.** The original comment
  claimed "the key prefix alone is already the effective namespace
  guard," which the schema does not actually support (`plugin_id` is the
  real namespace; the prefix is a sub-namespace within it). Reworded as
  part of the N1 fix.
- **N4 (test fidelity) — the regression test seeded via a SQL string
  literal, not the real write path.** `plugin_storage.value` is a `BLOB`
  column; a literal `INSERT ... VALUES ('...')` stores SQLite storage
  class `TEXT` instead, so the original test did not actually exercise
  the `BLOB`→JSON round trip through `scanGenericCols`'
  `[]byte`→`string` conversion — the reviewer independently verified the
  real path is correct, but flagged that the test didn't prove it.
  **Fix:** the fiscal-register row is now seeded via
  `PluginRepo.StorageSet(ctx, pluginID, key, []byte(...))`, the same
  method the real page handlers call, so `value`'s storage class is
  genuinely `BLOB` in the test as in production.
- **N5 (test coverage) — gate ordering was unpinned.** Every existing
  test used a manager actor, so swapping `requirePrimary` above
  `requireManager` (which would leak "this till is a replica" to an
  unauthorized non-manager via a 303 instead of a 403) would not have
  failed anything. Added
  `TestFiscalRegisterPage_NonManagerOnReplicaGetsManagerError`, which
  does pin the ordering.

### Investigated, not changed (accepted as-is)

- **N2 (efficiency) — `DumpAdmin` still runs `SELECT * FROM
  plugin_storage` and filters in Go, rather than pushing the `WHERE`
  into SQL.** This is the same shape the existing `plugin_settings` and
  `settings` entries already use (both filter in Go after a generic
  `SELECT *`), so this diff isn't introducing a new pattern, just
  extending an existing one. `plugin_storage` can in principle hold more
  data than those two tables (per-plugin KV, up to `StorageMaxKeys`/
  `StorageMaxValueBytes` each), so there's a real, if modest, efficiency
  argument for a future `dumpWhere`-style mechanism on `adminTable` — not
  done here, to keep this fix scoped to the actual correctness gap
  (N1) rather than a broader refactor of the dump mechanism affecting
  three existing table entries at once.
- **N6 (docs)** — addressed in this same session: added the
  `ut-docs/architecture/lan-sync.md` bullet described above, rather than
  leaving it for a follow-up card.

## Verified beyond automated tests

- **TDD, personally re-confirmed by the reviewer**: both new tests
  written first, confirmed failing pre-fix; the reviewer independently
  re-ran the revert→test→restore cycle for both the primary-gate and the
  scoped-sync claims and confirmed the same failing/passing behaviour.
- **N1's fix re-verified the same way** after the review: reverted the
  `plugin_id` scoping back out of `applyFiscalRegisterStorage`'s DELETE,
  confirmed the extended `TestAdminDumpApplyRoundTrip_FiscalRegisterStorage`
  failed with the expected "a different plugin's same-prefix row was
  removed" assertion, then restored and confirmed green again.
- **Empty-`recs` case** (every fiscal-register entry deleted upstream)
  traced by the reviewer and confirmed to clear all satellite rows under
  the scope with no error — covered by the shipped test's second
  (delete-propagation) phase.
- **No FK ordering concern**: `plugin_storage` has no FK constraints
  anywhere in the schema (grepped every migration; only `001_init.sql`
  ever touches the table), so its position in `adminTables`' FK-ordered
  list is inert.
- Full gate green throughout, before AND after the post-review fixes:
  `gofmt -l .`, `go build ./...`, `go vet ./...` (via `golangci-lint`),
  `go test ./...` (full suite, all packages), `golangci-lint run ./...`
  (0 issues), and all 18 CI-blocking guards from `ci.yml`'s `build` job
  (`guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`,
  `guard-page-http-error`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `guard-e2e-fixtures-import`,
  `check-brand-assets`, `guard-makefile-version`).
- Manual read in full, not just checked for existence, in all four
  locales (en/fa/tr/ar) — each accurately states both halves (mutations
  refuse on a replica; entries added on the primary now propagate
  automatically), with terminology consistent with each locale's
  established manual vocabulary.
- No real client/shop name or secret-shaped literal introduced anywhere
  in the diff.

## Verdict

**Safe to merge.** The compliance-critical property holds end to end:
§146a Abs. 4 AO fiscal-register data now reaches every till in the shop,
cannot be mutated on a replica, and — after the N1 fix — cannot be
corrupted, deleted, or exposed by any other installed plugin's own
`plugin_storage` state, in either sync direction.
