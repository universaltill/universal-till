# internal/data timezone sweep — third pass (ut-docs#1869)

**PR:** universaltill/universal-till (branch `fix/1869-tz-sweep-internal-data-tests`)
**Card:** universaltill/ut-docs#1869
**Complexity:** easy — Dev inline (Sonnet), Review via fresh-context Sonnet subagent

## What shipped

Follow-up to ut-docs#559 and ut-docs#1864: `internal/data`'s test suite still
wasn't timezone-clean. `TZ=Pacific/Auckland go test ./internal/data/ -count=1`
had 5 confirmed failures per the card's own repro. Verifying the fix against
the card's full required TZ set (UTC, Europe/London, America/Los_Angeles,
Asia/Tehran, Pacific/Auckland) surfaced 3 more instances of the exact same
bug class that weren't in the original list — fixed in the same PR since
they're the identical root cause the card's acceptance criteria targets.

Root cause, same as #559/#1864: a test hardcodes an expected day-key literal,
or picks UTC clock timestamps assuming they land on a particular side of a
local-midnight or business-day boundary — true only at small UTC offsets,
because the production queries (`SalesByDay`, `EndOfDay`/`dateRangeSummary`,
`WorkerAllocationsSummary`, `ListWorkerAllocations`) all group/filter via
SQLite's own `date(created_at, 'localtime', ...)`, not UTC.

Fixes, by file:
- `pos_repo_lifecycle_test.go` — `TestEndOfDay_FindsJournaledSaleOnOriginDay
  NotIngestDay` chose origin/ingest timestamps only 32 minutes apart
  straddling UTC midnight; widened to 48h apart, anchored at UTC noon (a
  gap no ±14h zone offset can collapse), and derived both query days via
  the existing `b8ExpectedDay` helper instead of hardcoding them.
- `worker_allocation_repo_test.go` — 4 originally-reported tests plus 2 more
  found during full-suite verification (`TestListWorkerAllocations_
  CashierFiltering`, `TestListWorkerAllocations_OrderedByAllocatedAtDesc`)
  all seeded `worker_allocations`/`payments` rows at UTC evening hours
  (18:00-20:00Z), which roll into the *next* local day at TZ=Pacific/
  Auckland's +12 offset while the query still used a hardcoded
  `"2026-08-25"` bound. Moved every such timestamp into the 07:00-12:00Z
  band (inside the same calendar day for every offset in this suite's
  required range, -8..+13) and derived every query day bound via
  `b8ExpectedDay`.
- `pos_repo_batch8_reports_test.go` — `TestPOSRepo_SalesByDay_
  BusinessDayBoundary_MergesTradingNight` (found during full-suite
  verification, not in the card's original list) hardcoded 23:30Z/01:30Z
  assuming both stay before the 04:00 local business-day cutoff; at
  TZ=Asia/Tehran (+3:30) the second timestamp crosses local 04:00, landing
  it on the *next* business day instead of merging. Anchored both
  timestamps in `time.Local` instead of fixed UTC clock hours, so they
  always straddle the host's actual local midnight and stay on the correct
  side of the cutoff regardless of host offset.

## Independent review (fresh-context Sonnet subagent)

Reviewed the diff cold, with no prior context on the fix's own reasoning.
Traced every touched query's production semantics against `b8ExpectedDay`'s
SQL expression, manually walked the offset arithmetic for all 5 required
zones on the relevant dates, and empirically re-ran the affected tests fresh
(bypassing Go's test cache, which does not key on `TZ`) under all 5 zones.

**No findings.** Specifically checked and cleared:
- No off-by-one in which local day two "adjacent" timestamps land on, for
  any of the 5 zones.
- `TestListWorkerAllocations_RoundTrip`'s exact-string `AllocatedAt`
  assertion was updated consistently with its seeded value.
- No remaining hardcoded UTC-anchored day-key literal in the touched files
  that could break under a further-out offset (the 3 remaining
  `"2026-08-25"` literals left untouched are pure error-path tests that
  never reach the query's date filter).
- gofmt and `go vet ./internal/data/...` clean.
- Comment accuracy (the specific offset arithmetic each comment claims)
  checked line-by-line against the actual math.

## Verified beyond automated tests

- `TZ={Pacific/Auckland,UTC,Europe/London,America/Los_Angeles,Asia/Tehran}
  go test ./internal/data/ -count=1`: all green except one pre-existing,
  unrelated failure under `America/Los_Angeles`
  (`TestGetPluginVersionAt`/`TestGetPluginVersionsAt_MatchesSingularSemantics`)
  — reproduced against `origin/main` in an isolated worktree, confirmed
  present before this branch's first commit, different subsystem
  (`plugin_repo.go`'s `InstallPlugin` stores `updated_at` via a bare
  `time.Now()` SQL arg instead of a UTC-normalized string) and different
  root cause (production code, not a test day-key literal) — filed
  separately as universaltill/ut-docs#1880 rather than folded into this
  fix or silently left for a future sweep to rediscover.
- `go build ./...` and `gofmt -l .` (whole repo): clean.
- `go test ./...` (whole repo, default host TZ): all green, no regression
  outside `internal/data`.
- `golangci-lint run ./internal/data/...`: 0 issues.
- Test-only change — no UI, money, offline-first, plugin-signing, or i18n
  surface touched; no migration, no API/DTO shape change.

## Safe-to-merge verdict

**Safe to merge.** Test-only diff, independently reviewed with no findings,
full required-TZ matrix green apart from one pre-existing unrelated failure
now tracked on its own card.

## Explicitly deferred

- universaltill/ut-docs#1880 — `GetPluginVersionAt`/`GetPluginVersionsAt`
  fail under `TZ=America/Los_Angeles` (pre-existing, different root cause,
  noted above).
