# country_settings admin LAN sync (ut-docs#1669)

## What shipped

`country_settings` (per-jurisdiction tax/currency/retention defaults,
`internal/data/country_settings_repo.go`) was flagged by #1586's
schema-drift guard as an unclassified table: it is manager-gated
(`requireManager` on the `"settings"` action in
`internal/pages/country_settings_page.go`) but **not** primary-only
gated, so a satellite till can edit it directly and diverge from the
primary today with nothing to catch it.

- Moved `country_settings` from `sync_admin_repo.go`'s `nonAdminTables`
  exclusion map into `adminTables` as
  `{name: "country_settings", pk: []string{"code"}}` — plain PK-only,
  no `hasIsActive`/`unique`, same shape as `settings`.
- Added a special case inside `ApplyAdmin`'s phase-2 per-row upsert
  loop: for `country_settings` rows, when `archive_min_days` is present
  and below `GlobalArchiveMinDays` (ADR-0040's 3650-day floor), it is
  clamped up before `upsertRow` writes it — because `upsertRow` writes
  columns raw and bypasses `CountrySettingsRepo.Upsert()`'s own floor
  validation entirely.
- Added `syncedDays(v any) int64`, a small type-switch helper
  (int64/float64/int) mirroring `bkpScanInt` in `bkp_products_repo.go`,
  since `archive_min_days` arrives as `int64` in-process but `float64`
  after a JSON wire hop.
- Two new tests in `internal/data/sync_admin_repo_test.go`:
  `TestAdminDumpApplyRoundTrip_CountrySettings` and
  `TestAdminApplyCountrySettings_ClampsArchiveMinDaysToGlobalFloor`.

Diff is confined to `internal/data/sync_admin_repo.go` and
`internal/data/sync_admin_repo_test.go`. No Go source outside
`internal/data`, no `web/`, no `internal/pages` touched.

## Independent review

Fresh-context subagent, isolated git worktree
(`git worktree add --detach /tmp/review-1669-worktree 04d6b933`, kept
entirely separate from the shared checkout). Read the full diff
(`git show 04d6b933`), the whole `adminTables`/`nonAdminTables`
mechanism and `DumpAdmin`/`ApplyAdmin`/`upsertRow`/`deleteMissing` in
`sync_admin_repo.go`, and all of `country_settings_repo.go`
(`Upsert`/`Delete`/`GlobalArchiveMinDays`), rather than taking the
commit message's claims on faith.

**Verdict: SAFE TO MERGE. No blocking findings.**

### What I independently verified

1. **`CountrySettingsRepo.Delete()`'s restore-not-remove semantics** —
   read the function directly: a `BUILTIN` code (`builtinCountryDefault`
   lookup succeeds) goes through `r.Upsert(ctx, def)`, restoring shipped
   defaults; only a genuinely operator-created code reaches the raw
   `DELETE FROM country_settings WHERE code = ?`. So a builtin row is
   never actually absent from a primary's dump, and hard-delete-on-prune
   for an operator-created row is the correct, intended behavior —
   confirms `adminTables`'s placement with no `hasIsActive` is correct,
   not just claimed correct.
2. **No FK risk on the hard-delete path** — grepped every migration for
   `REFERENCES country_settings` / an FK onto `country_settings(code)`:
   none exist. `deleteMissing`'s hard `DELETE` can never be FK-blocked
   here, so there's no silent "retired in place, has history" fallback
   this table secretly needs and doesn't have.
3. **The clamp only touches a genuinely-present column** — traced
   `upsertRow`: it iterates the replica's own live schema columns
   (`tableColumns`, `SELECT * LIMIT 0`), and for each one does
   `v, ok := rec[c]; if !ok { continue }` — a column absent from `rec`
   is dropped from the INSERT/UPDATE entirely (existing row: column
   untouched; comment: "column the primary doesn't know"). The new
   guard (`if v, ok := rec["archive_min_days"]; ok && ...`) mutates
   `rec` only when the key is present, so an older primary's bundle
   genuinely missing the column is left alone exactly as claimed — it
   does not force-add the column.
4. **`syncedDays` correctness** — int64/float64/int all convert
   correctly; a negative value converts correctly and (being `<
   GlobalArchiveMinDays`) gets clamped, which is the safe outcome; an
   unexpected type (nil, string — not realistically reachable from a
   NOT NULL INTEGER column through JSON, but checked anyway) falls to
   the `default: return 0` branch, which is *also* `< 3650` and so
   *also* gets clamped — the helper fails closed toward the compliance
   floor rather than failing open, which is the correct direction for a
   defense-in-depth check.
5. **TDD re-verification, exact task recipe** — `git checkout 04d6b933^
   -- internal/data/sync_admin_repo.go` (test file kept from the fix
   commit), ran both new tests: both **FAIL** —
   `TestAdminDumpApplyRoundTrip_CountrySettings` on "country_settings
   must appear in the admin dump now"; the clamp test on "synced
   country_settings row missing" (the whole table is unsynced pre-fix,
   so there's no row to check at all). Restored
   (`git checkout HEAD -- internal/data/sync_admin_repo.go`, working
   tree verified byte-identical to the commit via `git status`/`git
   diff --stat`): both **PASS**.
6. **Isolated the clamp claim specifically** (beyond what the task
   recipe alone proves): with `country_settings` left classified as an
   admin table but *only* the clamp `if` block removed, re-ran just
   `TestAdminApplyCountrySettings_ClampsArchiveMinDaysToGlobalFloor` —
   it fails with `got 30, want 3650 (ADR-0040 floor)`, i.e. the raw
   below-floor value from the hand-built bundle really does reach the
   database via the generic `upsertRow` path when the clamp isn't
   there. Restored via `git checkout HEAD --
   internal/data/sync_admin_repo.go`; re-ran both tests, both pass
   again; tree confirmed clean.
7. **Tests aren't vacuous against the 14 pre-seeded builtin rows** —
   traced each test:
   - The round-trip test explicitly *diverges* `GB`'s `tax_rate_bp`
     between primary (2200) and replica (1750) before syncing, so a
     pass proves the row actually traveled and primary won, not that
     primary/replica already agreed.
   - It also inserts a non-builtin `ZZ` row on both sides, deletes it
     only on the primary, and asserts it's gone from the replica after
     a second dump/apply — a real exercise of `deleteMissing`'s hard-
     delete path on a genuinely operator-created row, not a builtin.
   - The clamp test builds its `AdminBundle` by hand (not through
     `CountrySettingsRepo.Upsert`, which would itself refuse a
     below-floor value) for a non-builtin `ZZ` code with no pre-existing
     row — success can only come from `ApplyAdmin` actually writing and
     clamping it.
8. **`wireTrip` genuinely exercises the `float64` branch** —
   `wireTrip` round-trips the bundle through `encoding/json`
   marshal/unmarshal, which turns every JSON number into `float64`; the
   clamp test's `archive_min_days: int64(30)` therefore arrives at
   `syncedDays` as a `float64` in the actual test run, not just in a
   contrived unit test of the helper.
9. **No other special-case this table should have gotten but didn't** —
   checked against every other special-cased table
   (`settings`/`plugin_settings`/`plugin_storage`/`role_permissions`/
   `roles`/`permission_actions`):
   - No per-till dump-side filtering needed (unlike `settings`,
     `plugin_settings`, `plugin_storage`) — `country_settings` has no
     per-till/per-scope column at all; every row is genuinely shop-wide.
   - No `deleteMissing` skip needed for the *documented* reason those
     five get one (settings/plugin_settings/plugin_storage do their own
     scoped replace; roles/permission_actions/role_permissions guard
     against version-skew from a future custom-roles feature) —
     `country_settings` has no scoped-replace need, and its "custom
     rows" (operator-created, non-builtin codes) hard-deleting on prune
     is the *desired* behavior per `Delete()`'s own doc comment, not a
     gap.
10. **Full gate, run for real, in this worktree**:
    - `gofmt -l .` — clean.
    - `go build ./...` — clean.
    - `go test ./internal/data/...` — pass (29.4s).
    - `go test ./...` (full suite) — **all packages pass**, including
      `internal/pages` (126.4s) and `internal/plugins` (69.0s) — the
      flaky `internal/pages` failure the implementer saw on a prior run
      did **not** reproduce here; treating it as the same kind of
      transient resource-contention flake documented in the sibling
      #1671 review (concurrent test/build activity in the same
      environment), not a regression connected to this diff (this diff
      touches zero code outside `internal/data`, a different package
      tree from `internal/pages`).
    - `golangci-lint run ./...` (repo-wide) — 0 issues.
    - `bash scripts/ci/guard-data-access.sh` — passes (no SQL added
      outside `internal/data`/`internal/db`).

### Not applicable (checked, not assumed)

- No file writes in this diff — the two recurring bug classes (missing
  `os.MkdirAll`, a cwd-relative path instead of `paths.Data(...)`)
  don't apply.
- No real client/shop name — test data uses `GB` (already seeded) and
  `ZZ` (a fictitious ISO-reserved code), consistent with the rest of
  this test file's conventions.
- No UI/i18n/money-type/plugin-signing surface: confirmed via
  `git show --name-only 04d6b933` — exactly the two `internal/data`
  files changed, nothing under `internal/pages` or `web/`.

## Findings

None that block. One accepted-as-is observation, noted for the record
rather than fixed:

- **Accepted, not fixed — theoretical version-skew gap, same class as
  the one `roles`/`permission_actions` were explicitly exempted for.**
  `country_settings` has no `deleteMissing` skip for version skew. If a
  future release ships a migration that seeds an *additional* builtin
  country row (there is no such migration today — `builtinCountryDefaults`
  is a 14-entry compile-time list, and the only DB seed of
  `country_settings` is `001_init.sql`'s original 14 rows; nothing in
  this repo's current migration history adds more), and a satellite
  upgrades to that release before its primary does, the satellite's
  newer builtin row would be hard-deleted on its next admin pull (no
  `hasIsActive` fallback here), because the older primary's dump simply
  doesn't mention that code. This is not a regression introduced by
  this diff — it's the same latent framework-wide behavior every
  non-`hasIsActive` admin table already has under migration-version
  skew (e.g. `categories`, `translation_overrides`, `item_barcodes`)
  — and `roles`/`permission_actions` are the only two tables in
  `adminTables` that got a bespoke skew exemption, for a documented,
  concrete near-term feature (custom roles) that doesn't have a
  `country_settings` analogue today. Re-litigating the general
  skew-exemption policy for every admin table is out of scope for a
  card about classifying one table; flagging it here rather than
  silently deciding it's fine on my own is intentional, per this repo's
  document-first norm.

## Safe-to-merge

**Yes.** Build/test/lint/guard gate is green, the TDD claim held up
under re-verification (including a more granular isolation of the
clamp-specific claim beyond what the task's own recipe required), the
classification is correct against `CountrySettingsRepo.Delete()`'s real
semantics rather than its doc comment alone, and the two new tests
exercise real code paths against pre-seeded data rather than passing
vacuously. Nothing in this diff needed a fix.

## Deferred / out of scope

- The theoretical version-skew gap noted above — worth a follow-up
  only if/when a migration actually adds a new builtin country, not
  before.
- Gating `/country-settings` primary-only (the way `/registers` and
  `/locations` were gated in #1590) is a different, independent fix
  from "make it sync" — this card's own scope is the sync
  classification, not restricting where edits may originate.
