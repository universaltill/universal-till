# 2026-07-24 — Dev-mode marketplace override: validation, health-check, fallback, isolated TLS

## Context
Spec-audit gap (`ut-docs/QUEUE.md`, FR-015): `DevOverrideURL` routed all
marketplace traffic there unconditionally whenever set — never checked
`cfg.DevMode`, never validated the URL, never health-checked reachability,
no fallback-to-cloud-on-timeout, no self-signed-cert handling. The opposite
of the spec'd safety net for a dev-only escape hatch.

## Design
Found that the config surface for this was already fully built —
`cfg.DevMode`, `cfg.Marketplace.HealthCheckTimeoutSec`,
`cfg.Marketplace.FallbackTimeoutSec` all existed, wired to env vars with
"(FR-015)" comments — but completely unused anywhere in `internal/plugins/`.
Added `DevMode bool` to `MarketplaceConfig` itself (mirroring `Config
.DevMode`; the marketplace `Client` only holds `*MarketplaceConfig`) and
wired the rest into real behavior for the first time:

- **Startup validation + health check** (`NewClient`): the override is only
  ever used if `DevMode` is true, the URL parses as a real `http`/`https`
  URL with a host, and it passes one bounded `GET /healthz` — the same
  health endpoint the real marketplace exposes, unauthenticated. Any
  failure logs a warning and the client uses the cloud endpoint for its
  entire lifetime; no per-request re-checking.
- **Fallback-on-failure** (`doWithFallback`): when active, a request tries
  the dev override first (bounded by `FallbackTimeoutSec`, so a hung dev
  server can't stall a real request), and on any transport-level error
  (connection refused, timeout — NOT a non-2xx HTTP response, which
  propagates as-is) retries against the real cloud endpoint. `doRequest`
  (used by `IssueDownloadToken`/`AckDownload`/`ReportPluginStatus`) and
  `ListPlugins` (the live catalog-browsing path) both use it; `GetRevocations`
  (confirmed dead code, zero callers) got the cheap endpoint-selection fix
  for consistency but not the full fallback wrapping.
- Along the way: `ListPlugins` had a pre-existing bug (always sent
  `Authorization: Bearer <empty>` when no token was configured, instead of
  omitting the header like every other method does) — fixed.

## Independent review — caught two real problems in the first pass
Opus-model review, explicitly adversarial given the TLS-verification
surface. Both findings were empirically confirmed with throwaway tests
before I fixed them, not just argued.

**Fixed (both were real, both from one root cause — a single shared
`http.Transport`):**
- **MEDIUM — configuring an unhealthy/unused dev override silently
  disabled TLS verification for the real cloud endpoint.** The first
  version set `InsecureSkipVerify` on ONE shared transport whenever
  `DevMode && DevOverrideURL != "" && isValidHTTPURL(...) &&` it was
  `https://` — decided BEFORE the health check, and that transport backed
  every request the client made, dev override or cloud. Confirmed: `DevMode
  =true` + an unreachable `https://` override (health check fails,
  `devOverrideActive=false`, 100% of traffic goes to cloud) still ran every
  cloud request with TLS verification off. The code's own comment claimed
  this was "never for the real cloud endpoint" — false because of transport
  sharing. Fixed by splitting into two fully independent `*http.Client`s
  (`cloudClient`, `devClient`), each with its own transport; the bypass is
  only ever set on `devClient`'s transport, which is never used for cloud
  requests under any code path.
- **MEDIUM (functional) — a self-signed HTTPS dev override could never
  actually activate**, which is the primary scenario `DevOverrideURL`
  exists for (a LAN dev box). The health check built its own bare
  `&http.Client{Timeout: timeout}` with the default transport — no TLS
  bypass — so `GET /healthz` against a self-signed cert always failed
  verification, `devOverrideActive` stayed false forever, and the override
  was permanently unusable over HTTPS. Fixed by health-checking with the
  same `devClient` (and its TLS-bypass transport) that real requests will
  use.

**Confirmed correct (verified independently, not just accepted my claim):**
- No stale-state risk from `enroll.Effective(cfg)` — it copies the whole
  `Config` by value and only overwrites identity fields (DeviceID,
  ClientID, StoreID, PublicKey, MerchantToken); `DevMode`/`DevOverrideURL`/
  timeouts pass through untouched, and every real call site constructs a
  fresh `Client` per operation — no hot-reload/mutation hazard.
- The buffered-body retry in `doRequest` (`io.ReadAll` once, fresh
  `bytes.NewReader` per attempt) doesn't change behavior for any real
  caller — all three already pass small, already-in-memory
  `bytes.NewReader(marshaledJSON)`, never a streaming reader.
- `ListPlugins`'s empty-Bearer-header fix is correct and matches the
  existing "auth is optional" contract elsewhere in this file.
- `/healthz` on the real marketplace (`ut-cloud`) genuinely requires no
  auth (`AllowUnauthenticatedPaths`) and returns 200/503 correctly —
  verified by reading that repo directly, not assumed.
- Fallback triggering only on transport errors (not HTTP 5xx from the dev
  override) is a deliberate, reasonable design choice, not an oversight —
  now documented explicitly in `doWithFallback`'s comment.

## Verification
`go build ./...`, `go vet ./...` clean. `go test ./...` (full repo) clean.
New tests in `dev_override_test.go`: DevMode-off ignores a healthy override,
invalid URL ignored, unhealthy override ignored, healthy override activates,
a self-signed HTTPS override activates (regression test for finding 2), the
cloud client's transport never gets `InsecureSkipVerify` from merely
configuring an unhealthy dev override (regression test for finding 1), and
mid-session fallback when an active override dies after passing its startup
health check.
