# Review: an offline till says "not in the catalog yet" from a stale cached miss (ut-docs#3974)

Branch `fix/3974-stale-tax-catalog-miss`. Author: Sonnet (Dev subagent,
lane:cloud-24, complexity:easy). Independent review: Opus 5.5, fresh context,
in a separate worktree.

## What shipped

- `setupTaxCatalogEntries` (`internal/pages/setup_tax_catalog.go`) used to
  serve the last successful tax-catalog fetch on a failed refresh, however
  old. An hours-old fetch with no DE tax listing made
  `setupInstallableTaxPlugin` return nil instead of the Offline tile, so
  setup step 3 rendered the ut-docs#3244 queued branch: "The tax plugin
  isn't in the plugin catalog yet". The till was offline at the time, and the
  Settings chip said so.
- New `setupTaxCatalogStaleMax = 15m`. Every path that serves the cache
  without a fresh success goes through one closure, `serveCached`: the
  30-second retry window, the empty endpoint and a fetch error. Past the cap
  it returns `ok=false`, so the wizard shows the Offline tile. When the
  operator has already consented, `setup_page.go` then shows the
  queued-offline note. The cache is kept, and the next success replaces it.
- This is the one deliberate divergence from the language catalog, which
  still serves its cache indefinitely. The doc comments say so.
- No template, locale, migration or help changes. The existing
  `queued_offline` note is the right copy, and no help topic describes this
  message.

## Tests (`internal/pages/setup_tax_catalog_test.go`)

- `TestSetupInstallableTaxPlugin_StaleCachedMissReadsAsOffline_3974`
  (subtests `fetch_retried_and_fails` and `inside_the_retry_window`).
- `TestSetupInstallableTaxPlugin_RecentCachedMissStillServed_3974`: below
  the cap the cache is still served, which shows the cap drives the change,
  not "any failure".
- `TestSetupInstallableTaxPlugin_StaleCachedHitReadsAsOffline_3974`, added
  for review finding 2.
- `TestSetupGETStaleCachedMissOfflineShowsQueuedOfflineNote_3974`, the
  card's AC. It drives `GET /setup` through the real mux with a 3-hour-old
  cached miss, a dead marketplace and a queued DE spec. The page must show
  `data-tax-plugin-queued` and must not show the not-published copy.

TDD: the reviewer reverted `setup_tax_catalog.go` to main, keeping only the
const. The stale-miss unit test and the handler test then failed
(`plugin=<nil> unavailable=false`; the queued-offline note was missing and
the not-in-catalog text was present). After restoring the fix, all `_3974`
tests passed, including with `-count=5 -race`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | The fetch-failure warning said "serving nothing" when a cache existed but was past the cap | Fixed: it now logs "nothing: cache past the stale cap" |
| 2 | low | No test pinned a cached HIT past the cap becoming an Offline tile | Fixed: new `StaleCachedHitReadsAsOffline_3974` |
| 3 | low | The empty-endpoint path under the cap is untested | Accepted: it goes through the same `serveCached` closure as the tested paths |
| 4 | nit | The `installableTaxPlugin.Offline` field comment still said "nothing is cached" | Fixed |
| 5 | nit | The recent-cache test loops without `t.Run` | Accepted: harmless, and the failure message names the case |

Callers checked by the reviewer: the wizard render, the install handler (an
online till can never be past the cap, because every success resets
`lastSuccess`, so queuing rather than a foreground attempt only happens
offline), and the skip handler (a stale miss now queues on Skip, which is
correct). Nothing else calls the function.

## Verified beyond automated tests

No driven run on a real till. To reproduce, a first-boot till has to stay
offline for more than 15 minutes after a successful catalog fetch that found
no listing. The handler test renders that exact state through the real
router and template. Accepted gap.

Local gate: gofmt, `go build ./...`, `go vet`, `go test ./...`, and guards
core-neutral, data-access, i18n, page-http-error, kiosk-engine,
card-data-schema, pipefail-grep-q, netaccess and store-private all pass.
Two tools could not run in this container because their binaries are built
with Go older than the module's 1.27: `guard-deadcode-baseline.sh`
(go1.26) and golangci-lint (go1.25). CI covers both. The diff adds no new
top-level symbol except the const, which is used.

## Verdict

Safe to merge.
