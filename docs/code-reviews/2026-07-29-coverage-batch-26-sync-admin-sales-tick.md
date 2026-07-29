# Test coverage batch 26: sync_admin.go / sync_sales.go tick logic

2026-07-29

`internal/pages/sync_admin.go` (LAN sync D2b/D4, ADR-0011: primary-side
admin bundle + shop-wide stock endpoints, the D4 sync status chip, and the
replica-side drift-pull loop) and `internal/pages/sync_sales.go`'s
`StartSyncPush` (the replica-side sale-journal push loop) had zero test
coverage on their entry points — `registerSyncAdmin`, `runSyncLoop`,
`StartSyncPull`, and `StartSyncPush` were all 0%.

## Why this needed a small refactor first

`StartSyncPull` and `StartSyncPush` each wire a real 30-second
`time.Ticker` (`runSyncLoop`) to an anonymous closure holding all the
actual sync logic (fetch bundle, decide changed/unchanged, apply, update
settings, push a journal batch, advance the cursor). Because the closure
was anonymous and only reachable through the ticker, none of that logic
could be exercised in a test without a real 30-second wait per tick.

Extracted the closure bodies verbatim into two new named, unexported
functions — `syncPullTick(ctx, d, client, refresh)` in `sync_admin.go` and
`syncPushTick(ctx, d, client)` in `sync_sales.go` — with `StartSyncPull` /
`StartSyncPush` now just `runSyncLoop(ctx, func() { syncPullTick(...) })`.
No behavior change: same code, same order of operations, just callable
directly. Confirmed via the full pre-existing sync test suite
(`TestSyncEnroll*`, `TestSyncPromote*`, `TestSyncSnapshot*`,
`TestSyncJoin*`, `TestSyncSalesAPI*`) passing unchanged before writing any
new tests.

## Coverage added

**`sync_admin.go`** (`registerSyncAdmin` 0% → 86.4%, `syncPullTick` 0% →
71.0%, `withinLast` already covered elsewhere at 100%):
- `GET /api/sync/admin`: unauthorized rejection, full bundle on first
  fetch, `Unchanged=true` with an empty payload when `?have=` matches the
  current fingerprint.
- `GET /ui/sync-chip`: replica mode (stale/`warn` vs fresh/`ok`, till-name
  label), primary mode with no enrolled tills (empty chip), primary mode
  with an enrolled-but-never-seen till (`warn`) vs one that just
  authenticated (`ok`, count rendered).
- `syncPullTick`: no-op when unconfigured (verified with a
  request-refusing `http.RoundTripper`, not just an assertion on
  settings — proves it never even attempts a call); a changed bundle
  applies to the replica DB, updates `pull_version`/`last_pull_at`/
  `last_contact_at`, writes the `admin_pulled` audit row, and calls
  `refresh` exactly once; a second, unchanged-fingerprint tick does NOT
  re-apply or call `refresh` again; the stock-correction poll is skipped
  while this till still has local sales queued to push (and runs once the
  push queue is empty) — the exact ordering guarantee the code comments
  call out (an early stock pull could transiently re-add stock the
  primary hasn't counted yet).

**`sync_sales.go`** (`syncPushTick` 0% → 89.7%):
- No-op when unconfigured or when there are no local sales queued.
- A real push: local sale journaled to a real primary server, primary DB
  gains the sale, replica's `push_cursor`/`last_push_at`/
  `last_contact_at` all advance.
- **Offline-first retry guarantee**: when the primary rejects the push
  (wrong bearer → 401), `push_cursor` stays at its prior value so the
  next tick retries the same batch instead of silently dropping it.
  Deliberately asserted as its own test — this is the property that keeps
  an offline sale from ever being lost on a transient sync failure.

No real bug found this batch (unlike 11, 23, 24) — the tick logic behaved
as documented once it was actually testable.

## Independent review

Not yet performed — see follow-up note in the commit; this doc will be
updated (or a follow-up doc added) once the second-model review lands,
per the standing workflow.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
