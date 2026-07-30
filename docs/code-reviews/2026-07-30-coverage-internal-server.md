# Code review — coverage batch 3: `internal/server` (2026-07-30)

**Change**: test-coverage batch 3 of the queue's "drive `universal-till`
toward ~100%" item (same series as PRs #89/#90). `internal/server`
6.4% → **89.0%** of statements (85.7% under `-race`; the delta is
goroutine-timing branches). One modified file (`internal/server/server.go`),
one new test file (`internal/server/server_test.go`).

## Real bug found TDD-first (medium)

`syncCatalog` hardcoded `deviceArch := "linux/amd64"` while **every**
interactive path sends `runtime.GOOS + "/" + runtime.GOARCH`
(`plugins_store_page.go:77`, `cloudsync_wire.go:149`, `plugin_api.go:762`,
plus `installer_store.go`/`installer_marketplace.go`/`cloudsync.go:275` —
independently re-grepped by the reviewer). Consequence on the field Pi
(linux/arm64): whenever the 15-minute scheduler found the cache stale, it
re-fetched with `arch=linux/amd64` and **overwrote the catalog cache with an
amd64-filtered snapshot** — wrong plugins/artifacts on the store page until
a manual refresh. Proven red first on darwin/arm64
(`requested arch "linux/amd64", want "darwin/arm64"`), fixed via a new
`deviceArchOf` seam (see below), green after.

## Production changes beyond the fix (all behavior-preserving seams)

- `retryBaseDelay` field on `BackgroundJobs` (default 1s in
  `NewBackgroundJobs`, defensive fallback in `syncCatalog`) so backoff tests
  don't sleep for real.
- Retry backoff made **ctx-aware** (`select` on `ctx.Done()` vs
  `time.After`) — also a genuine shutdown-latency improvement: shutdown no
  longer waits out up to 3s of blind `time.Sleep`.
- Daily-backup closure extracted to package func `runDailyBackup` (logic
  unchanged, now directly tested).
- `startBrowser` package var wrapping the browser-exec glue so no test can
  ever spawn a real browser; the exec body itself is documented untestable
  glue.
- `deviceArchOf` package var (reviewer's should-fix, see below).

## Hermeticity (this series' hard bar) — proven, not claimed

Full suite passes with `HTTP_PROXY`/`HTTPS_PROXY` pointed at a dead port
(any off-loopback call would die); all network is loopback `httptest`; all
browser-touching tests stub `startBrowser`; DB/backup writes confined to
`t.TempDir()`; `-race` clean.

## Tester mutation probes (3)

1. Stale-gate removed (sync unconditionally) → `SkipsWhenFresh` red. Caught.
2. Backup skip-when-fresh removed → **initially ESCAPED**: `Snapshot()`
   reuses a same-second filename, so the count-based assertion couldn't see
   a duplicate snapshot. Test hardened (rename the first backup to an
   older-timestamped name + `os.Chtimes` backdate, assert name unchanged) —
   re-probed, caught. A real false-pass found and fixed in this batch's own
   test, before review.
3. ctx-aware backoff regressed to blind `time.Sleep` →
   `AbortsBackoffOnShutdown` red. Caught.

## Independent review (different model, opus): SAFE TO MERGE

Re-ran build/vet/`-race` suite and the poisoned-proxy hermeticity check
itself; re-proved the TDD arc by hand-reverting only the arch line
(exact claimed failure reproduced, then green after restore); ran **3 fresh
mutation probes** (host-normalization delete, last-resort rebind of the busy
port, `retryBaseDelay` init removal) — 3/3 caught. Combined with the
tester's probes and the TDD arc: 7 probes + 1 arc, one initial escape
(tester's own #2), fixed and re-proven; zero escapes in final state.

Findings:

1. **[should-fix, FIXED pre-commit]** The arch regression test was
   platform-blind on linux/amd64 CI runners — `runtime.GOOS/GOARCH` there
   equals the old hardcoded value, so a re-hardcoding would keep CI green.
   Fixed exactly as the reviewer recommended: `deviceArchOf` package var;
   the test pins a sentinel (`sentinelos/sentinelarch`) and asserts verbatim
   pass-through (catches re-hardcoding on **every** platform — re-proven red
   against a re-hardcoded `syncCatalog` after the fix), plus a direct
   `TestDeviceArchOf_ReportsRuntime` asserting the default computes from
   runtime.
2. **[nitpick, comment added]** `TestListenWithFallback_LastResortAnyFreePort`
   has a vanishingly-rare TOCTOU window (a port busy at hold time freeing
   before the call under test) — documented in the test.
3. **[nitpick, accepted]** `startBrowser`/`log.SetOutput` seams rely on the
   package's tests staying non-parallel (they are; none use `t.Parallel()`).
4. **[nitpick, accepted]** `syncCatalog`'s `baseDelay <= 0` fallback is
   belt-and-braces; the field is always initialized by the constructor.

Also verified by the reviewer: no SQL in the test file (repo rule), no real
shop/client names, no literal credentials.

## Honestly-untestable remainder (documented, not faked)

- `startBrowser`'s real body — spawns an actual OS browser process.
- The daily-backup goroutine's timer wiring (2-min first check, hourly
  ticker) — `runDailyBackup` itself is 100% covered; the wiring is
  wall-clock waits.
- Related-items 24h ticker + rebuild-failure branch; `supervisor.Shutdown`
  branch (needs a live WASM supervisor); `srv.Serve` non-graceful error
  return; `listenWithFallback`'s port-0 exhaustion error path (requires the
  OS to be out of ephemeral ports); the `catalogRepo != nil` wiring in
  package `Start` (the composed `BackgroundJobs` paths are covered directly).

## Verified beyond the new tests

Full repo suite (30 packages) green; all 5 CI guard scripts pass; gofmt/vet
clean. Remaining coverage targets for the parent queue item:
`plugins/storage` (39.3%), `ui` (26.7%).
