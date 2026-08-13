# Review — takeaway_rate_overrides merge race (ut-docs#532)

## Summary

`mergeTakeawayOverrides` (`internal/pages/import_page.go`) did an unguarded
Get-then-Upsert read-modify-write on ut-plugin-tax-de's
`takeaway_rate_overrides` plugin setting (a JSON object, tax_code_id →
takeaway basis points). Two catalog imports committing close together
could both read the same starting JSON, both compute a merge, and the
second write silently clobber the first's additions — low probability on
a single-till POS host, but a real consequence when it happens: a missing
override means wrong takeaway VAT with no error anywhere. Found during
independent review of ut-docs#512 (universaltill/universal-till#285).

## Changed here

- New `PluginRepo.MergeAdditiveJSONMapSetting` (`internal/data/plugin_repo.go`)
  — runs the read, merge and write inside one transaction. The DSN's
  `_txlock=immediate` (ut-docs#311) makes `BeginTx` issue `BEGIN IMMEDIATE`,
  taking the SQLite write lock *before* the SELECT, so a second concurrent
  caller can't start its own read until the first has committed — the same
  pattern already established in `settings_repo.go`'s `GetOrCreate`/
  `SetMany`. Existing keys (e.g. a merchant's hand-set override) still
  always win over a newly discovered value for the same key, and an
  existing value that fails to parse as JSON is still left completely
  untouched — both pre-existing guarantees, now provable atomic.
- `mergeTakeawayOverrides` simplified to a thin wrapper calling the new
  repo method and translating an error into the pre-existing
  `(added, failed)` + `log.Printf` contract — behavior at every one of the
  five outcome paths (read error, invalid JSON, added==0, marshal error,
  write error) is unchanged.
- New regression test file `internal/data/plugin_repo_merge_setting_test.go`:
  20-way concurrent-merge test (asserts all 20 land), an
  existing-entry-preserved test, and an invalid-existing-JSON test.

## TDD evidence

Confirmed RED before GREEN, twice independently:

- **Dev**: reverted `MergeAdditiveJSONMapSetting` to a naive
  `GetPluginSetting`+`UpsertPluginSetting` pair and ran the concurrency
  test 3×: it failed every time, losing 16-18 of 20 entries. Restored the
  fix, re-ran: passed, including under `-race`.
- **Independent reviewer** (Opus, isolated worktree, did not trust the
  Dev-reported result): repeated the same revert-then-restore
  independently with `-count=5`: 5/5 failures pre-fix (losing 17-18 of 20
  entries each run), pass at `-count=5` and `-count=3 -v` post-fix.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `go test -race ./internal/data/...` — `ok` (reviewer's run: 356s).
- `go test ./internal/pages/...` (pages, pages/catalog, pages/common) — `ok`,
  including the four pre-existing `TestImport_TaxOverrides*` tests
  unmodified (merge/preserve, invalid-JSON-warns, plugin-not-installed,
  plugin-disabled).
- `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh` — all green.

## Independent review (Opus, isolated worktree)

**Verdict: PASS, safe to merge.** All findings non-blocker; fixed the two
cheap ones, deferred one to a follow-up card, and this record closes the
fourth (missing review record).

- **Fixed — scope read/write asymmetry documented.** The read prefers the
  most-specific scope present (register > user > global, same as
  `GetPluginSetting`), but the write always targets `scope='global'`
  (matching `UpsertPluginSetting`'s own global-only default). This is
  **pre-existing behavior, preserved rather than introduced** — the old
  `GetPluginSetting`+`UpsertPluginSetting` pair had the identical
  asymmetry, latent today because `takeaway_rate_overrides` is
  global-scoped only. Reviewer confirmed the consequence empirically with
  a throwaway test (since deleted): a register-scoped row present for the
  key causes the merge to read it but write the result into a *separate*
  global row, under-reporting to `GetPluginSetting`'s own callers. Added a
  doc comment on the method spelling this out; the actual behavior change
  (scoping the SELECT to `scope='global'`, or accepting scope as a
  parameter) is real future work for whichever caller would actually need
  non-global scoping — filed as a new Backlog card
  (universaltill/ut-docs#668) rather than expanding this fix's scope.
- **Fixed — no-op no longer takes the write lock.** Added
  `if len(newEntries) == 0 { return 0, nil }` before `BeginTx`, so an
  all-duplicate merge no longer takes `BEGIN IMMEDIATE`'s write lock just
  to do a read-only no-op. Unreachable from the one current caller
  (`import_page.go` already guards on `len(takeawayOverrides) > 0`), but
  cheap and correct for the method as a reusable primitive.
- **Noted, not changed — method owns its own transaction.** No injectable
  `tx *sql.Tx` parameter (unlike `InstallPlugin`'s `executor(tx)`
  pattern); matches `settings_repo.go`'s `GetOrCreate` precedent exactly.
  Documented in a comment: a future caller invoking this while already
  holding a write transaction on the same DB will block for
  `busy_timeout(5000ms)` then fail `SQLITE_BUSY` rather than nest.
- **This record** — the review's fourth finding was simply that it didn't
  exist yet.

Reviewer also confirmed: no deadlock/starvation risk (short, non-nested
transaction; contention surfaces as the existing best-effort
`failed=true` summary warning, catalog rows already committed regardless);
first-committed-wins semantics on a same-key race across two concurrent
callers (consistent with the documented "existing entry always wins"
rule, `added` count stays accurate); no missing `os.MkdirAll` or
cwd-relative path (no file I/O in this diff at all); no real client/shop
name or secret anywhere in the diff.

## Docs required

None beyond this record — no ADR (implementation-level concurrency fix,
contradicts nothing accepted), no `web/help/` update (no shop-owner-visible
behavior changed — same summary line, same warning text, same counts), no
i18n impact (only string touched is a server log line), no README impact.

## Verdict

Safe to merge. One follow-up filed: universaltill/ut-docs#668 (scope the
`MergeAdditiveJSONMapSetting` SELECT to `scope='global'`, or make scope a
parameter, before any second caller adopts it against a non-global-only
key).
