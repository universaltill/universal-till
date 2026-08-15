# Review: wasm hostHTTPRequest buffer-ABI retry no longer re-issues the live call (ut-docs#754)

**Date**: 2026-08-15
**Card**: universaltill/ut-docs#754 — "wasm hostHTTPRequest: buffer-ABI
retry re-issues the HTTP request, not just re-fetches — non-idempotent
calls can double-fire"
**Complexity**: medium
**Reviewer model**: Opus subagent, fresh context, worktree-isolated (per
this card's `complexity:medium` tier — see `scrum-master` skill's model
routing)

## What shipped

`hostHTTPRequest` (`internal/plugins/wasm_hostfns.go`) implements the
plugin module's buffer ABI: a call returns the FULL response length, and
if that exceeds the guest's `dstCap` the guest is expected to "call again
with a bigger buffer," passing the identical request bytes. Every other
buffer-ABI call re-derives its answer idempotently on that retry (a
SQLite read, or — since ut-docs#614 — a bounds-checked stream cursor).
`hostHTTPRequest` did not: the retry re-issued the live outbound HTTP
call, so a payment/ERP-connector plugin POSTing a charge or order with an
undersized response buffer could duplicate that side effect purely from
following the documented ABI. Follow-up from the independent review of
ut-docs#614 (finding M2, deferred as this card).

The fix adds a single-slot cache (`httpCacheReq`/`httpCacheResp`) to the
per-event `hostState` struct — `WasmRuntime.HandleEvent` creates a fresh
`hostState` per event/module instantiation, so the cache dies with the
instance and can never leak across events or plugins. `hostHTTPRequest`
checks the cache (exact byte match on the request) before doing any work;
on a hit it replays the cached response. On the network path, it stores
into the cache **only when this call's own response overflowed `dstCap`**
— the one case the ABI's own contract says "call again" — and a cache hit
**clears itself** the moment it's served into a buffer big enough to hold
the whole response, so the cache only ever covers the immediate pending
retry, never a later, separately-motivated call with the same bytes.
Failed calls (bad request, denied, network error) are never cached.

TDD-first: `TestHostHTTPRetryDoesNotReissueRequest` proves the reported
case — an undersized-buffer call followed by a same-bytes bigger-buffer
retry hits a counting `httptest.Server` exactly once.
`TestHostHTTPRetryDifferentRequestNotCached`,
`TestHostHTTPRepeatSameRequestBothReachServer` and
`TestHostHTTPRetryThenFreshCallNotCached` are false-positive guards (see
Independent review below — two of these were added *because of* what
review found, not before). Confirmed failing against the pre-fix code
(exact reported symptom: 2 server hits for one logical retried call)
before confirming passing against the fix, by both the implementer and,
independently, the reviewer.

`ut-docs/reference/plugin-host-functions.md` documents the new guarantee
and its scope.

## Independent review (Opus, fresh context, worktree-isolated)

**Initial verdict: NOT SAFE TO MERGE.** First-pass diff cached every
successful call unconditionally, with no expiry. Demonstrated live (not
theoretical): the reviewer temporarily repointed the existing
different-URL test at the same URL twice — two genuinely separate,
adequately-buffered calls (never an undersized-buffer retry) — and got
exactly the bug this fix was meant to close: the second call was served
from cache, never reaching the server. Concrete production scenarios: a
connector polling `GET /job/status` in a loop within one event would see
the first response forever; a connector retrying an identical request
after a transient 5xx would get the cached failure back instead of a
fresh attempt; a deliberate duplicate submission would be silently
swallowed. All silent, no error, nothing to debug from.

### Findings triaged

- **F1 (fixed, was BLOCKER)**: cache was unconditional and never expired
  — see above. Fixed by gating the store on `len(out) > dstCap` (only the
  actual overflow case populates the cache) and clearing the cache the
  moment a hit is served into a buffer that fully satisfies it. Verified
  with two new regression tests: `TestHostHTTPRepeatSameRequestBothReachServer`
  (two same-bytes, always-adequately-buffered calls both reach the
  server, distinct response bodies) and
  `TestHostHTTPRetryThenFreshCallNotCached` (miss → retry-hit-and-clear →
  a third same-bytes call is a fresh miss, distinct body from the second).
- **F2 (resolved as a side effect of F1)**: an earlier concern that
  non-2xx responses were cached like any other "success," which would
  pin a `503` a connector meant to retry. Once caching is gated to the
  overflow case only, an adequately-buffered retry-after-error is never
  cached, so this doesn't arise in practice; not a separate code change.
- **F3 (fixed)**: a comment block had been accidentally duplicated
  verbatim during the implementer's own TDD revert/restore cycle.
  Rewritten (not just de-duplicated) as part of the F1 fix.
- **F4 (fixed)**: `ut-docs/reference/plugin-host-functions.md` originally
  stated the duplicate-side-effect guarantee in absolute terms ("not
  possible"). Reworded to state precisely what's covered (the immediate
  overflow retry with byte-identical request bytes) and explicitly what
  isn't (a deliberate second call, even with identical-looking bytes,
  once the first call's buffer already fit) — with a pointer to building
  a real idempotency key/nonce for plugins that need that guarantee.
- **F5 (fixed)**: `readGuest`'s doc comment claimed it "copies" guest
  memory; it returns a live view (verified against wazero v1.12.0
  source). Pre-existing, but the new `bytes.Clone` this fix depends on
  now directly contradicts the old wrong comment — left in place, it
  would read as an invitation to "simplify away" a clone the fix needs.
  Corrected to state plainly that the caller must copy anything that
  needs to outlive the call.
- **F6, F7 (deferred, both nits)**: F6 — a guest-fixture `reqJSON` slice
  isn't referenced after its last `ptrOf` call, so the test's premise
  (the same guest memory still holds the same bytes across two host
  calls) isn't made explicit with `runtime.KeepAlive`; low risk, and if
  it ever broke the test would fail loud, not silently pass. F7 — a
  cache hit returns before `CheckPermission` runs again, so a permission
  revoked in the ≤10s window between the miss and the immediate retry
  would still be honored for that one retry; no audit-log gap (nothing
  was denied to audit). Both accepted as-is: neither affects correctness
  of the guarantee this card exists to provide, and process-depth
  guidance is to fix what matters rather than grind on nits.

### Explicitly checked and confirmed fine (from the review's own report)

- **Memory aliasing**: `bytes.Clone(raw)` is both correct and necessary
  — `m.Memory().Read` returns a live view into wasm linear memory, not a
  copy (verified against wazero v1.12.0 source); `s.httpCacheResp = out`
  holds a Go-owned `json.Marshal` buffer, no aliasing. The clone happens
  before `writeGuest`, so an overlapping `dstPtr`/`reqPtr` can't corrupt
  the cache key.
- **Concurrent host calls against one `hostState`**: not possible.
  `hostState` is allocated fresh per event in `HandleEvent`
  (`wasm_runtime.go:470`); guests are WASI commands run synchronously via
  one `InstantiateModule` call per event; the wazero threads proposal is
  not enabled anywhere in this module. Two concurrent events get two
  distinct `hostState`s. `-race` clean across 17 tests / ~5.5 minutes.
- **`bytes.Equal` semantics**: sound — `nil` vs populated is guarded by
  `s.httpCacheResp != nil`, an empty request can never populate the
  cache (fails `json.Unmarshal` first), exact byte comparison so no
  hash-collision risk.
- **"Only cache success"**: all 11 early-return paths in
  `hostHTTPRequest` sit above the (now overflow-gated) cache-store line;
  none populate the cache.
- **`httpResponseCap` (256 KiB) interaction**: unaffected — the cached
  `out` already holds the truncated body, so a retry sees the identical
  truncated result.
- **Wire-visible ABI**: unchanged — same signature, same response JSON
  shape, same return semantics. Confirmed by the pre-existing
  `TestHostFunctions`/`TestHostHTTPWildcardNet`/`TestHostSettingsGet`
  passing untouched.
- **Two recurring pipeline bug classes**: no file-write handler added
  (N/A for missing `os.MkdirAll`); no cwd-relative path (tests use
  `t.TempDir()` throughout).
- **No secrets, no real client/shop name**: grepped the diff; only test
  literals (`com.test.httpretry`, `/charge`, `/a`, `/b`, `127.0.0.1`).

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on every changed file —
  clean (implementer and reviewer, independently, both before and after
  the F1 fix).
- `go test ./internal/plugins/... -run 'TestHostHTTP|TestHostFunctions|
  TestImportDispatch|TestHostTCP|TestHostSettings' -race -v` — 19 tests,
  all pass, no data races, ~5.5 minutes.
- Whole-repo `go test ./...` (43 packages) — green.
- `bash scripts/ci/guard-data-access.sh` / `guard-kiosk-engine.sh` /
  `guard-plugin-menu-read.sh` / `guard-i18n.sh` — all pass (N/A scope
  confirmed: no SQL, no self-order/kiosk route, no plugin-menu read, no
  user-facing strings — this is an internal plugin-runtime fix).
- TDD claim re-verified independently by the reviewer: disabled both
  cache blocks, rebuilt, reran `TestHostHTTPRetryDoesNotReissueRequest`,
  confirmed it fails with the exact claimed symptom (2 server hits, not
  1), restored, confirmed clean (`git status --porcelain` /
  `git diff --stat` both empty) and passing again.

## N/A for this diff

No i18n strings, no money-type conversion, no plugin manifest change, no
self-order/kiosk route, no user-facing UI, no `web/help/` manual topic —
internal plugin-runtime robustness fix; the wire-visible `http_request`
semantics for a well-behaved guest are unchanged.

## Safe to merge

Yes, after the F1/F3/F4/F5 fixes above. F2 resolved as a side effect of
F1. F6/F7 deferred as genuine nits — neither affects the correctness of
the guarantee this card exists to provide.
