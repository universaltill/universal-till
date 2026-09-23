# 2026-09-20 — Turkey yüzde usulü pool collection-side mechanism (ut-docs#988, ADR-0063 step 2/2)

## What shipped

The Turkey collection-side mechanism ADR-0063 Decision 3 explicitly
deferred: a manager-facing way to record "we collected X today under
yüzde usulü" with no customer-facing bill line, and to distribute it to
named workers, replacing the tautological "received == allocated by
construction" placeholder the existing `worker_allocations` report used
for this `source_type` since ut-docs#987.

- New migration `internal/db/migrations/037_yuzde_usulu_pool_collections.sql`
  — `yuzde_usulu_pool_collections` (+ `_archive` twin, ADR-0042 §1 shape
  copied from `worker_allocations_archive`: no PK/UNIQUE on the archive,
  only the `reset_batch_id` FK) and its indexes.
- `internal/data/yuzde_usulu_pool_repo.go`: `InsertYuzdeUsuluPoolCollection`,
  `ListYuzdeUsuluPoolCollections`, `YuzdeUsuluPoolCollectionsTotal`,
  `GetYuzdeUsuluPoolCollection` (found-bool convention, same as
  `AuthRepo.GetUser`).
- `WorkerAllocationsSummary`'s `yuzde_usulu_pool` case
  (`internal/data/worker_allocation_repo.go`) now reads "received" from
  the new collections table instead of summing `worker_allocations`
  against itself — the tautology this card exists to remove. "Allocated"
  is unchanged (`worker_allocations`, this `source_type`).
- `internal/data/reset_archive_repo.go`: `yuzde_usulu_pool_collections`
  wired into `resetArchiveTables` and `restoreEmptyCheckTables`, same
  no-FK reasoning as the neighboring `worker_allocations` entry.
  `internal/data/sync_admin_repo.go` classifies both new tables in
  `nonAdminTables` (required by `TestSchemaTablesAreClassified`).
- `internal/pages/reports_page.go`: new
  `POST /api/reports/worker-allocations/pool-collections` endpoint
  (record a collection); the existing
  `POST /api/reports/worker-allocations` endpoint now accepts
  `source_type=yuzde_usulu_pool` with a required `pool_id`, validated via
  `GetYuzdeUsuluPoolCollection` before the distribution is written; the
  existing CSV export merges in `yuzde_usulu_pool` rows alongside
  tip/service_charge. The `tip`/`service_charge` path is otherwise
  byte-identical (`source_id` still always `""`, same response shape).
- New tab "yuzde-usulu" (`web/ui/partials/reports_tab_yuzde_usulu.html`,
  `renderYuzdeUsuluTab`), Turkey-only
  (`"ShowYuzdeUsulu": d.CurrentState().Country == "TR"` gates the tab
  button in `reports.html`, same `{{ if .IsManager }}`-style pattern as
  the existing `eod` tab). Collected-vs-distributed KPIs, a collections
  table + record form, a distributions table + record form (worker +
  pool picker), an export link.
- i18n: 34 new `reports.yuzde_usulu.*`/`reports.tab.yuzde_usulu` keys,
  genuinely translated (not copy-pasted, not machine output) in all four
  `web/locales/{ar,en,fa,tr}.json`.
- Manual: new "Yüzde usulü pool distribution (Türkiye)" section added to
  `web/help/{en,de,fa,tr,ar}/reports.md` with real translated prose;
  `scripts/ci/i18n-baseline/help-drift-baseline.json` updated honestly
  (the pre-existing structural gap on this topic is unchanged in size on
  every locale, just re-recorded at its new absolute values — checked
  arithmetic, not just copied forward). `make docs-shots` run; only
  `web/help/img/manifest.json`'s surface hash changed (the demo shop is
  GB, so the new Turkey-only tab button doesn't render in any captured
  screenshot — expected, not a gap).
- Tests: migration+archive round trip, repo CRUD round trips including
  date-range exclusion, a `WorkerAllocationsSummary` test proving
  Received and Allocated can now legitimately differ (impossible to
  express before this card), HTTP-level tests for both endpoints
  including permission-denial and unknown/missing `pool_id`, an export
  test confirming pool rows are merged in.

## Independent review

Opus, fresh context, no visibility into the Dev reasoning — reviewed the
raw working-tree diff cold. Ran the full gate for real: `gofmt -l .`,
`go build ./...`, `go test -count=1 ./internal/data/... ./internal/db/...
./internal/pages/...` (plus `-race` on the new page tests),
`golangci-lint run ./...` (0 issues), `go test
./internal/db/... -run TestShippedMigrationsUnchanged -v`, and all 30
guards referenced by `ci.yml`'s `build` job. Additionally ran **10
deliberate mutations** of production code against the suite in a
throwaway copy (since deleted) to check the tests actually fail when the
behavior they claim to cover is broken — 9/10 caught cleanly.

**One blocker, fixed:** `web/help/img/manifest.json`'s `surface_sha256`
was stale (recorded a hash that didn't match the actual rendered
surface), and — worse — zero PNGs had actually been regenerated despite
the diff adding a new tab button and a whole new tab fragment.
`guard-docs-shots.sh` failed for real (confirmed the failure is
introduced by this diff, not pre-existing, by running the same guard
against a pristine `HEAD` checkout first). Fixed by running
`make docs-shots` for real (Playwright, pre-installed Chromium) rather
than the `update-docs-shots-surface-hash.sh` escape hatch, which is only
legitimate when a change alters no rendered pixel. Verified after the
regen: guard green, and — correctly, not suspiciously — only
`manifest.json` changed, because the demo shop used for screenshots is
GB and the new tab is Turkey-only, so no captured page actually shows it.

**One correctness fix, medium severity:** the `pool_id` existence check
in the distribution endpoint (`internal/pages/reports_page.go`) collapsed
`err != nil || !found` into a single 400, discarding the exact
error/not-found distinction `GetYuzdeUsuluPoolCollection`'s own doc
comment promises its caller. A real DB fault (e.g. `database is locked`
during a concurrent reset) would have been reported to the operator as
"pool_id must be a recorded pool collection" — their own mistake — with
nothing reaching the log, unlike every other DB failure in the same
handler which does go through `common.LogAndLocalizedError`. Split into a
real 500 (logged) for a query error and a 400 only for a genuine
not-found.

**One test-quality gap closed, medium severity:** the reviewer
mutation-proved that deleting either `worker_allocations` or
`yuzde_usulu_pool_collections` from `restoreEmptyCheckTables`
(`internal/data/reset_archive_repo.go`) left the entire suite green —
the archive round-trip tests exercise the happy path but nothing had
ever exercised the restore-refusal path for either table. Concrete
failure this guards against: a manager records a payout/collection after
a reset but before an old batch is restored, and the restore silently
*merges* the archived batch with that new live row (different ids, no PK
collision — ADR-0042 §2 says restore must never merge). Closed with
`TestRestoreRefusesWhenNewWorkerAllocationOrPoolCollectionSinceReset`
(`internal/data/reset_test.go`), table-driven over both tables — this
also closes the pre-existing gap on `worker_allocations` inherited from
ut-docs#987, not just the new table. Re-verified independently here (not
just taken on the reviewer's word): reverted `restoreEmptyCheckTables` to
drop both entries, confirmed the new test fails on both subtests with the
exact expected-vs-got message, restored the file, confirmed green again.

## Confirmed clean, no action needed (reviewer's findings, independently spot-checked)

- **Money**: `amount_minor` is `int64` end to end
  (`strconv.ParseInt`/`COALESCE(SUM(...),0)`), no float anywhere in the
  new code; no double-counting between "received" (collections table
  only) and "allocated" (`worker_allocations` only).
- **Authorization**: both new write paths gate server-side on the
  existing `worker_allocation` permission before anything else — no new
  permission needed, matches the card's own non-goal. No IDOR: the one
  path that accepts a `pool_id` always calls `GetYuzdeUsuluPoolCollection`
  first.
- **UK path unchanged**: `sourceID` stays `""` for `tip`/`service_charge`,
  `InsertWorkerAllocation` receives identical arguments, the audit detail
  keeps its former shape (`source_id` added only when non-empty), the CSV
  export merge is purely additive.
- **Migration**: exactly one new numbered file; `001_init.sql` and every
  prior migration untouched; the checksum added to
  `shippedMigrationChecksums` is genuine (appears once, and the pinned
  test passes for real — a copy-pasted/wrong value would still "look"
  present but fail the test).
- **i18n**: all 34 new keys present in `ar`/`fa`/`tr`, zero identical to
  the English value, full key sets still match `en.json` exactly. Spot
  checks confirm real, idiomatic translations (Turkish `adisyon`,
  pre-numeral `%30`; Farsi Persian digits/percent sign; properly
  diacriticized Arabic).
- **No compliance-claim violations** in any of the five languages —
  copy consistently describes only what the software records, and
  explicitly disclaims deciding whether/when a shop runs a pool.
- **RTL**: the new partial's inline CSS uses only logical
  properties/`text-align:end` — no `left`/`right`.

## Deferred (new Backlog candidates, not this card's scope)

- Over-distribution against a single pool is not enforced (nor was it
  asked to be) — consistent with ADR-0063 Decision 4's "inert unless
  used" design, but the existence-check comment in
  `reports_page.go` should note explicitly that this is deliberate so a
  future reader doesn't assume the check does more than it does.
- `renderYuzdeUsuluTab` swallows read-side errors into a bare zero
  (`summary, _ =`, etc.), same pre-existing pattern `renderTipsTab`
  already uses — on a surface whose whole purpose is surfacing a
  shortfall, rendering "£0.00" indistinguishably from a genuine empty
  period is the worst failure mode to have silently. Worth a follow-up
  covering both tabs together, not scoped to this card.
- Both endpoints' validation error strings are hardcoded English
  (`http.Error(w, "date must be YYYY-MM-DD", ...)`), same as the
  pre-existing tip/service_charge endpoint — `guard-i18n.sh` can't see
  raw `http.Error` bodies. Inherited pattern, lands harder on a
  Turkey-only tab; worth fixing across both endpoints together.
- Two very-low translation nits flagged by the reviewer
  (`reports.yuzde_usulu.col_recorded_by` in `ar.json` reads as a verb
  phrase rather than a column header; `fa.json`'s "worker" word choice is
  valid but uncommon) — meaning is not obscured either way, a polish item
  for a future pass rather than this card's.
- `ut-plugin-tax-tr` (deciding *whether*/*when* a shop uses yüzde usulü,
  or its percentage) remains explicitly out of scope, per ADR-0063
  Decision 4 and this card's own non-goals.

## Verified beyond automated tests

- Full `go test -count=1 ./...` (every package in the repo, not just the
  touched ones) green after all three fixes above, re-run a second time
  from scratch (not cached) to confirm.
- `golangci-lint run ./...` and every guard referenced above re-run
  clean after the fixes, not just once before them.
- `make docs-shots` run for real (Playwright against pre-installed
  Chromium), 124/124 shots passing, guard green.
- The restore-refusal fix was mutation-tested by hand (not just added
  and trusted): confirmed it fails without the underlying guard,
  confirmed it passes with it restored.

## Safe to merge

Yes.

## `lang-pack-drift` follow-up

`en.json` gained 34 keys — a follow-up PR is needed in each of
`ut-plugin-language-de`/`ut-plugin-language-es` per `CLAUDE.md`'s
lang-pack-drift convention. `lang-pack-drift` is advisory-only on this
PR (it only blocks a push to `main`); filing the pack follow-ups is the
merging lane's responsibility per `ut-docs`' scrum-master skill, tracked
separately from this record.
