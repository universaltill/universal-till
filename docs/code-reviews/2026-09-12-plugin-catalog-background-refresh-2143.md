# Code review: `/plugins` catalog blocking on a dead network route (ut-docs#2143)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2143
**Branch:** `fix/2143-plugins-catalog-background-refresh`
**Complexity:** medium (Dev: Sonnet inline, Review: Opus subagent in an isolated worktree)

## What shipped

Filed as a follow-up from ut-docs#2131's own review (Finding 5,
`docs/code-reviews/2026-09-11-plugin-catalog-hasupdate-2131.md`): ut-docs#2131
made `CatalogRepository.GetOrFetch` refetch the marketplace catalog whenever
its 15-minute on-disk cache had gone stale — correct in isolation, but the
refetch was fully synchronous and inline, and `Fetch` held `CatalogRepository`'s
single `sync.RWMutex` for the **entire** network round-trip. On a LAN with a
dead/blackholed route (not a clean connection-refused), that blocked
`/plugins`, `/plugins/store`, and update-listing resolution
(`resolveListingViaCatalog`) for up to the marketplace client's own
`RequestTimeoutSec` (30s) — and because the mutex was held for the whole call,
concurrent page loads serialized into sequential 30s waits instead of one.

`internal/plugins/marketplace/catalog_repository.go`:

1. `Fetch` no longer holds `cr.mu` across the network call — only across the
   brief update to `cr.cached` and the on-disk snapshot afterward.
2. `GetOrFetch`, on a **stale-but-present** cache, now serves that stale
   snapshot immediately (`isStale=true`) and kicks off a **background**
   goroutine (`refreshInBackground`) to refetch, instead of blocking on it.
   The **no-cache-at-all** path is unchanged in shape (still synchronous —
   there is nothing to serve otherwise).
3. `refreshInBackground` uses a `refreshing bool` (guarded by `cr.mu`) so a
   second/third caller arriving while a background refresh is already in
   flight just serves the same stale snapshot rather than starting another.
4. The background goroutine deliberately uses `context.Background()`, not the
   triggering request's context (which is cancelled the instant its handler
   returns) — the marketplace client's own `RequestTimeoutSec` still bounds
   it.
5. A new `coldFetch singleflight.Group` (`golang.org/x/sync/singleflight`,
   already an indirect dependency, now direct) coalesces concurrent
   **no-cache-at-all** callers sharing identical `(locale, deviceArch)`
   parameters into one network request — added during review (Finding 3
   below), not part of the original cut.

No i18n/help/UI changes — this is a backend latency fix with no new
user-facing string or screen.

## Independent review (Opus subagent, isolated worktree)

Ran fresh: read the diff and the three `GetOrFetch` call sites
(`plugins_page.go`, `plugin_api.go`'s `resolveListingViaCatalog`,
`plugins_store_page.go`'s `PluginStoreHandler`), built/vetted/linted, ran the
marketplace and relevant pages suites under `-race`, and — the load-bearing
step — **independently reverted the fix and re-ran the new regression tests
against pre-fix code**, confirming both failed for the claimed reason
(one blocked 5.0s on the blackholed listener; the other panicked with
`test timed out after 20s`, i.e. deadlocked), then restored the fix and
confirmed green again, `-race -count=3`, 46/46 PASS.

**Verdict: safe to merge, one round** — no blocker-class (money/tax,
data-loss, security) issue found. Ten findings, triaged below.

### Fixed

1. **Flaky regression test (real).** `TestGetOrFetchRefetchesWhenStale`'s poll
   loop compared the server's hit counter, which increments at the *start* of
   the handler — before the background goroutine has written `cr.cached`.
   Reviewer reproduced an intermittent failure at ~0.8% under `-race -cpu=1`
   with 8×150 parallel iterations. **Fix:** poll `repo.Get()`'s own staleness
   instead of the hit count (that's what the test is actually asserting).
2. **No-duplicate-refresh test passed vacuously (real).** The assertion was
   `maxConcurrent <= 1`, which a fully-disabled background refresh
   (`maxConcurrent == 0`) also satisfies — reviewer proved this by
   sabotaging `refreshInBackground` to a no-op and watching the test still
   pass 5/5. **Fix:** wait for at least one request to actually arrive before
   releasing it, then assert `maxConcurrent == 1` and `totalRequests == 1`
   exactly, not just an upper bound.
3. **Cold-start thundering herd (real, minor regression).** Removing the
   whole-call mutex (item 1 above) also removed the *accidental* serialization
   it gave the no-cache-at-all path: reviewer measured 5 concurrent
   `GetOrFetch` calls on a fresh till (no snapshot yet) going from 1 network
   round-trip pre-fix to 5 with this diff alone. **Fix:** `coldFetch
   singleflight.Group`, keyed by the exact `(locale, deviceArch)` pair (not a
   single shared key — `internal/server/server.go`'s own comment already
   documents a real, previously-shipped bug from mixing up per-arch catalog
   results, so coalescing must never hand one caller another caller's
   differently-filtered snapshot). New regression test
   (`TestCatalogRepository_GetOrFetch_NoCacheAtAllCoalescesConcurrentCallers`)
   independently TDD-verified: reverted to 5 requests without the fix,
   confirmed 1 with it restored.
4. **No `recover()` in the detached goroutine (real, minor).** Repo
   convention for a background goroutine that outlives its triggering request
   is an explicit `recover()` (`plugin_update_scheduler.go`'s
   `pluginUpdateCheckTick`, established by the ut-docs#1953 review, with the
   same rationale: an unrecovered panic in a goroutine takes down the whole
   till process, not just this one refresh). `refreshing` itself was already
   safe either way (the `defer` clearing it runs even on panic) — this closes
   the actual gap, a crash with no test/lint catching its absence. **Fix:**
   added, logged and swallowed, matching the existing pattern.
5. **Stale doc comment (nitpick).** `plugins_page.go`'s comment above the
   `GetOrFetch` call still described the old synchronous-refetch behavior.
   **Fix:** updated to describe the stale-serves-immediately/background-
   refresh behavior and cite both cards.
6. **Log level (nitpick).** Background-refresh failure logged at `[DEBUG]`;
   the package's own convention (`client.go`) uses `[WARN]` for a comparable
   failure. **Fix:** both new log lines now `[WARN]`.
7. **Test goroutine/conn leak + cross-test log bleed (nitpick, but with a
   concrete repro).** The blackhole-listener test's accepted connections were
   never closed, and reviewer observed this test's own delayed background-
   refresh failure log line print inside a *later* test's output. **Fix:**
   track and close every accepted connection via `t.Cleanup`, and lowered
   that test's `RequestTimeoutSec` from 5s to 1s (still long enough that a
   500ms elapsed-time assertion is unambiguous) to shrink the window further.

### Accepted, not fixed (deliberate, documented rather than silently shipped)

8. **`resolveListingViaCatalog` (`plugin_api.go:720`) can miss a
   just-published listing on the first Update click.** A plugin imported from
   a file (no `plugin_install_status` row) whose listing was published within
   the last stale window: pre-fix, the inline refetch would have found it;
   post-fix, the first click serves the stale snapshot, `Resolve` misses, and
   the user sees *"Plugin has no marketplace listing"* once — a second click
   after the background refresh lands succeeds. This is the direct, intended
   consequence of the fix's own acceptance criteria ("never block on an
   unreachable network call"), not a bug introduced by it, and is largely
   masked in practice because rendering `/plugins` itself already warms the
   cache before a user can click Update. Noted here rather than silently
   possible; not something to change without relitigating the AC itself.
9. **`internal/server.go`'s scheduler (`syncCatalog`, line 155) calls
   `cr.Fetch` directly, bypassing both the `refreshing` flag and the new
   `coldFetch` singleflight** — so a scheduler tick racing a page-triggered
   refresh can now interleave two genuinely-concurrent network calls (pre-fix
   the whole-call mutex fully serialized them). Because `FetchedAt` is
   stamped right after each call's own network response returns, but the
   brief lock that actually writes `cr.cached` is acquired independently of
   response-arrival order, the *older* fetch can now (rarely) be the one
   written last — self-correcting, since the cache then simply goes stale
   again sooner, and sharpened by the two callers already using different
   locale/arch parameters (a pre-existing mismatch from ut-docs#2131, not
   introduced here). Fixing this properly means deciding whether the
   scheduler and the page-handler paths should share one coalescing key
   despite requesting different parameters — a real design question, not a
   quick patch, and out of this card's scope. **Filed as
   universaltill/ut-docs#2155** rather than silently left for the next person
   to rediscover.

## Pre-existing gap, confirmed not caused by this change

The reviewer separately found that `go test ./internal/plugins/ -race` does
not complete (times out) — on **both** this branch and `main` at HEAD,
identically (`panic: test timed out after 4m0s` on each, apples-to-apples).
The surviving goroutine is in `plugins/tax-tr/okc/sim`, the Turkish fiscal
device simulator — unrelated to `CatalogRepository`. `internal/plugins`'s
only contact with the catalog repo is `update_checker_test.go`'s
`NewCatalogRepository(nil, cacheDir)` via `UpdateChecker.Get()`, which is
byte-identical before and after this diff and passes green under `-race` in
isolation. Flagged here so it isn't mistaken for something this change
introduced; not this card's to fix — filed as
universaltill/ut-docs#2156.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...`, `golangci-lint run
  ./internal/plugins/... ./internal/pages/...` — all clean (0 issues).
- `go test ./internal/plugins/marketplace/... -race -count=8` and `-count=5`
  — stable, no flakes, across two separate stress runs (before and after the
  post-review fixes).
- `go test ./internal/pages/... -race -run
  'TestPluginsPage_|TestPluginStore|TestApplyPluginUpdate|TestHandleUpdatePlugin'`
  — green (all three `GetOrFetch` call sites' own tests, under `-race`).
- Full `go test ./...` (no `-race`, whole repo) — green.
- CI-blocking guards run locally: `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh` (only
  pre-existing, already-baselined drift), `guard-compliance-claims.sh`,
  `guard-docs-shots.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-page-http-error.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh` — all green.
  `guard-shellcheck-version.sh` couldn't run in this sandbox (no `shellcheck`
  binary on `PATH` here — an environment gap, not a code issue); real CI runs
  on `ubuntu-latest`, which carries it, so this isn't skipped there.
- TDD claims independently re-verified by the review subagent for both
  originally-shipped regression tests (blocking-on-unreachable and
  no-duplicate-refresh) and, in this orchestrator's own follow-up pass, for
  the new cold-start-coalescing test added during triage — each reverted,
  confirmed red for the claimed reason, restored, confirmed green.

## Deferred / follow-up

- universaltill/ut-docs#2155 — scheduler/page-handler `Fetch` call
  coalescing and the locale/arch parameter mismatch between them (Finding 9
  above).
- universaltill/ut-docs#2156 — `go test ./internal/plugins/ -race` hangs on
  `plugins/tax-tr/okc/sim`, pre-existing on `main`, unrelated to this diff.
