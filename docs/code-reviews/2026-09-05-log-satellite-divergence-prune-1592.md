# Log a satellite-local registers/stock_locations row being pruned (ut-docs#1592)

**Card:** universaltill/ut-docs#1592 — "deleteMissing should log when it
prunes a pre-existing satellite-local registers/stock_locations row
(ut-docs#1590 upgrade path)" (found during independent review of #1590,
filed separately per that review's recommendation).
**Repo/branch:** universal-till, `fix/1592-log-satellite-divergence-prune`
**Complexity:** easy (Dev inline at Sonnet, Review via a fresh-context
Sonnet subagent — one round, one real finding, fixed in-session).

## The gap

`SyncAdminRepo.ApplyAdmin`'s phase-1 `deleteMissing` prunes any row a
table's PK set no longer includes in the primary's dump. `registers` and
`stock_locations` joined the synced set in #1590, once their admin pages
gated create/rename/activate to primary-only. On the first admin pull
after a shop upgrades past #1590, a register or stock location a manager
had created directly on a satellite under the old, ungated behaviour is
either hard-deleted (no history yet to FK-block it) or retired in place
(deactivated + renamed, if it does have shift/inventory history) — with
**zero log line either way**. A shop owner or support engineer seeing "my
till lost its register" has no way to connect the report to this one-time
reconciliation.

## What shipped

`internal/data/sync_admin_repo.go`:

- New `logSatelliteDivergencePrune(t adminTable, args []any, action
  string)` helper, called right before the `continue` on both success
  paths inside `deleteMissing` — the immediate hard-delete, and the
  FK-blocked retire-in-place fallback. Gated to fire only for
  `t.name == "registers"` or `"stock_locations"`; every other adminTable
  prunes routinely, so logging those too would bury the signal in noise.
  Warnf names the table, the row's PK value(s), and which action was
  taken, and points at ut-docs#1590 for context.

`internal/data/sync_admin_repo_test.go`:

- Extended the two existing FK-blocked tests
  (`TestAdminApply_RegisterRetiredInPlaceWhenFKBlockedBySatelliteShiftHistory`,
  `TestAdminApply_StockLocationRetiredInPlaceWhenFKBlockedBySatelliteInventoryHistory`)
  to also assert the new Warnf fires, via `logging.ResetRecent()` /
  `logging.Recent()` — the process-local ring buffer the `logging` package
  already exposes specifically for this kind of test assertion.
- Two new tests for the hard-delete (no-history) path:
  `TestAdminApply_RegisterHardDeletedPreExistingLogsWarning`,
  `TestAdminApply_StockLocationHardDeletedPreExistingLogsWarning`.
- One negative test, `TestAdminApply_OrdinaryTablePruneDoesNotLogSatelliteDivergenceWarning`,
  driving a `tax_codes` row through the identical hard-delete code path and
  asserting no divergence warning fires — guards the table-name scoping
  itself, not just the two positive cases.

`web/help/{en,fa,tr,ar}/multitill.md`: one new bullet in the "Registers"
section explaining that a pre-existing satellite-local register/stock
location will be removed or deactivated on the first sync after
upgrading, and pointing at **Settings → Tills → This till's register** if
a joined till's own register goes stale as a result.

## Independent review — one round, fresh-context Sonnet, one real finding

Spawned as a `general-purpose` subagent (`model: sonnet`, fresh context,
no prior involvement), reviewing the live `git diff` on the branch. It
re-ran the full gate itself (`gofmt -l`, `go build ./...`, `go test
./internal/data/...`, `golangci-lint run ./internal/data/...`) and
checked: minimality/correctness of the source change, whether the tests
would actually fail if the fix were reverted, CLAUDE.md compliance
(repository pattern, i18n, money, offline-first), and structural
consistency of the four locale edits — including checking each locale's
literal button-label phrase against its `web/locales/<locale>.json`
`settings.tills.register_label` value.

**Finding 1 (LOW, fixed) — the new Turkish bullet named the wrong UI
label.** `web/help/tr/multitill.md`'s new line used **"Bu kasanın
kasası"**; the actual button/select (`web/locales/tr.json`,
`settings.tills.register_label`) reads **"Bu cihazın kasası"** — the exact
phrase the pre-existing bullet one line above already uses correctly for
the same English source ("This till's register"). A Turkish-reading shop
owner following the new troubleshooting bullet would look for a control
that doesn't exist under that name. Not caught by any guard
(`guard-help-topics.sh`/`guard-i18n.sh` check structure and key parity,
not translation-label correctness against the locale JSON). Fixed by
correcting the bullet to match the existing label; the fa/ar equivalents
were checked against the same locale file and were already correct.

**Two notes, accepted as-is, no code change (both LOW/advisory):**

- The Warnf fires before `ApplyAdmin`'s `tx.Commit()`, so if a *later*
  table in the same call fails and rolls back the whole transaction, this
  warning would already be logged even though the DB change was undone —
  mirrors the pre-existing pattern of every other Warnf already inside
  this same function (the "cannot prune" line, the duplicate-plugin-
  settings lines), not a new risk this diff introduces.
- The log message's premise ("this prune means a pre-#1590 satellite-
  local row") holds today — confirmed via repo-wide grep that no
  production path ever hard-deletes a `registers`/`stock_locations` row —
  but would misattribute a future genuine primary-side delete feature to
  the #1590 upgrade path if one is ever added. Worth remembering, not
  actionable now.

## Verified beyond automated tests

- `gofmt -l internal/data/sync_admin_repo.go internal/data/sync_admin_repo_test.go`:
  clean. `go build ./...`: clean. `golangci-lint run ./...` (whole repo):
  0 issues.
- `go test ./...` (whole repo): all packages pass.
- All 19 CI-blocking guard scripts from `.github/workflows/ci.yml`'s
  `build` job re-run locally (`check-brand-assets.sh` through
  `guard-webkit-version.sh`, `guard-help-topics.sh` included): all pass.
- TDD re-verified personally, twice: reverted just the two
  `logSatelliteDivergencePrune` call sites in `sync_admin_repo.go` (test
  file left as-is) and confirmed the 4 positive tests fail for real
  (`--- FAIL`) while the negative test still correctly passes; restored
  and confirmed all 6 green again. The reviewer subagent independently
  re-confirmed correctness by reading the call sites and reasoning about
  `%v` formatting on the single-column PK `args` slice, rather than
  re-running the same revert.
- No migration touched. No money, offline-first, or plugin-taxonomy
  surface engaged. No i18n JSON key needed — `web/help/**` is
  locale-specific prose, not routed through `{{ T }}`/`web/locales/*.json`
  (confirmed by both the BA/Architect scoping and the reviewer,
  independently, against the existing `web/help/{en,fa,tr,ar}` structure).
- No real client/shop name anywhere in the diff.

## Safe to merge

Yes. No blocker survived review. The one real finding (Turkish label
mismatch) was fixed in-session and re-verified (`guard-help-topics.sh`
re-run clean); the two advisory notes are pre-existing patterns or
forward-looking premises, neither actionable today, and are recorded here
rather than deferred to a new card since there's nothing to act on yet.
