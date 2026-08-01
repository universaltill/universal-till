# Code review — coverage batch 14: internal/alerts, internal/lookup, internal/plugins/oauth

- **Date:** 2026-07-31
- **Branch:** `test/coverage-batch-14-lookup-alerts-oauth`
- **Scope:** test-only + one small testability seam (~650 lines across 3 test
  files; production change limited to `internal/alerts/alerts.go`'s `Start`
  swapping two hardcoded timer literals for a test-overridable
  `firstDelay()`/`tickInterval()` pair — identical pattern to the existing
  `internal/cloudsync` seam, same defaults, no behaviour change)
- **Card:** ut-docs#9 (coverage push remainder), batch 14
- **Independent reviewer:** Opus subagent (different model from the pipeline's),
  ran build/vet/tests itself including `-race`, `-count=5 -shuffle=on`, and its
  own 9-mutation pass (plus 2 sub-variants) on top of the pipeline's 3 spot-checks

## What shipped

| Package | Before | After |
|---|---|---|
| `internal/alerts` | 76.6% | **97.5%** |
| `internal/lookup` | 75.8% | **98.5%** |
| `internal/plugins/oauth` | 77.1% | **96.4%** |

Highlights:
- `alerts.Start`'s actual loop body (previously only its cancel-before-first-fire
  join was tested) now runs for real via the new firstDelay/tickInterval seam,
  proving BOTH the low-stock digest push and the unusual-sales push fire and
  the loop ticks again.
- `runningOutCount`/`pushDigest`'s error branches are exercised with real
  `DROP TABLE` fault injection (dropping `sale_lines` vs `inventory` distinctly,
  the latter only reachable after seeding a real sale so `ItemDailySellRates`
  succeeds first) rather than mocking the repo layer.
- `lookup.FetchImage`'s success/non-200/transport-error paths now run through
  a real HTTP round-trip via a `rewriteTransport` `http.RoundTripper` that
  presents an allowlisted hostname to the SSRF check while redirecting bytes
  to a local `httptest.Server` (no fake TLS cert needed).
- `oauth.TokenClient.GetToken`'s concurrent-caller coalescing (one network
  request for N simultaneous callers on a cold cache) is verified two ways:
  a real-race test proving the aggregate property, and a deterministic
  lock-barrier test (hold `tc.mu.Lock()` before spawning callers so they all
  queue at the same `RLock()` and are released together) that specifically
  drives the in-memory double-check-after-lock branch.

## Mutation evidence (tests fail when the code is broken)

Pipeline pass (3 spot-checks, all caught): `pushDigest`'s `n==0` guard flip,
`FetchImage`'s non-200 status check removal, oauth's double-check-after-lock
removal (see finding 2 — this one initially did *not* fail, which became the
first review finding).

Reviewer pass (9 mutations + 2 sub-variants): 7 of 9 caught cleanly
(`weeks<3` threshold, `firstDelay()` reverted to hardcoded, `ListStockLevels`/
`ItemDailySellRates` error-swallowing, `FetchImage` User-Agent/status-check
removal, `lookupOne` treating non-404-non-200 as `ErrNotFound`). 2 did not
fail on the first pass and became findings 2–3 below (both resolved).

## Review findings and outcomes

1. **(should-fix, fixed)** The original deferred-coverage note claimed
   `alerts`'s remaining gaps were unreachable `http.NewRequestWithContext`
   construction-error branches — factually wrong (those were already covered).
   The real gap was `Start`'s loop body only exercising the low-stock digest
   push, never the unusual-sales push. `TestStart_RunsDigestLoopBody` extended
   to also seed a 4-week baseline + blowout-yesterday scenario and assert both
   push types land on the fake marketplace before the loop is cancelled.
2. **(should-fix, fixed)** `TestTokenClient_GetToken_ConcurrentSingleFlight`'s
   comment overclaimed that it drives the write-lock double-check "for real" —
   mutation-tested false: removing that exact branch left the test green,
   because GetToken has two other redundant single-flight layers (whichever
   goroutine wins the race to lock first does the fetch; the disk-cache
   fallback also rescues a late arrival). The test is not vacuous (it does
   verify the real "N callers → 1 request" property, and mutating away all
   three layers together does fail it) but the comment was corrected to say
   so precisely, and a new deterministic test
   (`TestTokenClient_GetToken_DoubleCheckAfterLock`) added: holding `tc.mu.Lock()`
   before spawning 8 callers forces them all to queue at the same `RLock()` and
   be released together, guaranteeing genuine contention on the write lock and
   driving the double-check branch specifically. No production change.
3. **(should-fix, fixed)** `lookup_test.go`'s pre-existing `TestFetchImageAllowlist`
   (predates this batch, but this batch touches the same file) only asserted
   `err != nil` — mutation-tested false: disabling the SSRF allowlist check
   entirely left the test green, because the bad URLs then failed with a
   *network* error instead (wrong host/connection-refused), not a refusal. On
   a CI runner with open egress, a deleted guard would go undetected AND the
   request would actually be made. Fixed to assert the refusal reason
   (`"must be https"` / `"not allowed"`) so a deleted guard fails loudly.
4. **(nit, fixed)** `TestRequestToken_DefaultExpiryWhenNoExpiryFieldsGiven`
   accepted a 10-minute window against a 55-minute constant (a drift to e.g.
   52min would pass silently). Tightened to bracket the call's own
   before/after timestamps against the exact `+55min` default.

## Honestly deferred (confirmed by independent review)

- `lookup.lookupOne`'s `http.NewRequestWithContext` error branch on an
  already-parsed, allowlist-passed URL — genuinely unreachable with these types.
- `oauth.requestToken`'s `json.Marshal` on a `map[string]string`, and
  `saveToDisk`'s `json.MarshalIndent` on `CachedToken` — cannot fail for these
  concrete types.
- `oauth.getDeviceID`'s `"pos-device-1"` fallback — requires `os.Hostname()`
  to fail, which doesn't happen in any CI/container environment reliably.
- A couple of `Warnf`-only log lines inside `alerts.Start`'s loop (the
  pushDigest/pushNotify *failure* logging, as opposed to the success paths
  now covered) — the underlying error-propagation behavior is already
  covered by the direct `pushDigest`/`pushNotify` error tests; only the
  log-line call site from inside the loop itself remains untested.

## Verified beyond automated tests

- Full `go test ./...`, `go vet ./...`, both CI guards
  (`guard-data-access.sh`, `guard-i18n.sh`) green before and after review
  fixes. The one failure in the full suite (`internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure`) is pre-existing and unrelated —
  confirmed identical on a clean `main` worktree; caused by tests running as
  uid 0 in this sandbox (root bypasses the read-only-directory permission
  check the test relies on), not by this diff.
- `-race` and `-shuffle=on` (repeated 5× by the reviewer, 3× by the pipeline):
  no flakiness, no order dependence.
- The `firstDelay()`/`tickInterval()` seam verified to preserve production
  defaults exactly (`2*time.Minute`/`24*time.Hour`), package-private, no
  public API change, structurally identical to `cloudsync.go`'s existing
  pattern.
- No secrets, no real client/shop names (fixtures: "Cola"/"Widget"/"Floor",
  placeholder tokens like `tok-1`/`shared-token`/`store-x`).

**Verdict: safe to merge.** Reviewer's findings fully applied and
mutation-re-verified; remainder documented above.
