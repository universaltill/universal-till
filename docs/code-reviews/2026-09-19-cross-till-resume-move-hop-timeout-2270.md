# Review: bound serial write-through hop stacking on resume/move hot paths (universaltill/ut-docs#2270)

**Card:** Split out of ut-docs#1920 (ADR-0093, cross-till held-sale
write-through sync) by that card's own independent review — a
latency-budget follow-up, not a correctness bug. Each cross-till
write-through proxy call has an ~800ms timeout budget, fine in isolation,
but several run serially on one request in `internal/pages/hold_api.go`:
a busy-basket resume against a reachable-but-dropping-packets primary
cost up to ~3.2s worst case, and the held/table move handler up to
~2.4s-4s (see "What the review found" below — the true pre-fix move
figure turned out higher than the issue's own quoted number).

**Complexity:** medium. Dev at Sonnet (inline), review at Opus (fresh
subagent, isolated worktree).

## What shipped

- `internal/pages/hold_api.go`: a new `crossTillHotPathProxyTimeout`
  constant (300ms) plus a `context.Value`-based marker
  (`withCrossTillHotPathTimeout` / `crossTillHotPathNetCtx`, unexported
  key type) that `resumeHeldSale` and the `POST /api/pos/held/table` move
  handler set once, at the top of the request, on the `ctx` they were
  handed.
- Three low-level HTTP-calling helpers check for the marker and, when
  present, derive a short-lived **child** context bounded to 300ms
  *only* for their own `client.Do(req)` call — `postTableClaimOnPrimary`
  (`tables_claim_proxy.go`), `postHeldSaleOnPrimary` and
  `fetchHeldSalesFromPrimary` (`held_sale_sync_proxy.go`), and
  `fetchTablesFromPrimary` (`tables_sync_proxy.go`, added during review —
  see below). The outer `ctx` used for every local SQLite fallback
  read/write is deliberately **never** given a deadline: every one of
  these functions falls back to a local repo call on that same `ctx` when
  the network leg fails, and a `ctx` already past its own deadline fails
  a `database/sql` call immediately — which would have turned the local
  offline-first fallback itself into a spurious error exactly when the
  primary is unreachable. This was caught and fixed mid-implementation,
  before it ever reached a diff, not left for review to find.
- Two new regression tests in `hold_cross_till_test.go`:
  `TestResumeHeldSale_HotPathBoundedWhenPrimaryIsSlow` and
  `TestHeldTableMove_HotPathBoundedWhenPrimaryIsSlow`, each standing up a
  real primary (`httptest.Server`) wrapped in an artificial
  `hotPathTestPrimaryDelay` (700ms, strictly between the new 300ms budget
  and the proxy clients' shared 800ms `Timeout` — asserted as an explicit
  invariant in both tests, not just a comment) and asserting both that
  the request completes well under an unbounded-regression figure and
  that the local fallback actually landed (a read a genuine effect, e.g.
  the resumed engine snapshot / the held_sales row's new `table_id` /
  the held_sales row's actual deletion), not just a 200 response.
- Chose option 2 from the issue's own three suggested approaches
  (shorten the timeout budget) over option 1 (run independent hops
  concurrently): the move handler's claim-new-table-before-releasing-old
  ordering is explicitly load-bearing per its own existing comments
  (avoids a window where a table reads free when it shouldn't), so
  reordering into concurrent hops was judged too risky for this card's
  scope. This diff is purely a timeout-budget change — the hop ordering
  is byte-for-byte unchanged.

## What the review found (Opus, isolated worktree)

**First pass verdict: NO, not safe to merge as-is** — the mechanism was
sound but incomplete, and the doc comment's own latency claim was
measurably false on one path. Both blockers were fixed same-session, and
the fixes were themselves independently re-verified (see below).

1. **BLOCKER, fixed: a 5th hop on the move path was still unbounded.**
   `renderHeldStripWithToast`, which every exit path of the move handler
   calls to re-render the held-sales strip, derives its own `ctx` from
   `r.Context()` — a *different* `ctx` than the one the handler marked
   locally, so the marker never reached it. That render calls
   `tablesWithStateForDisplay` → `fetchTablesFromPrimary`
   (`tables_sync_proxy.go`), which was not one of the three helpers the
   first draft patched, and so kept its full 800ms budget regardless of
   the fix. Measured empirically by the reviewer (`elapsed` instrumented,
   then reverted): un-fixed move path 3.512s (5×700ms), "as first
   committed" (missing this bound) 1.906s, fully fixed 1.506s (5×300ms).
   The constant's own comment claiming "~1.2s (move)" was therefore
   false in the direction that mattered — the real as-first-committed
   figure (1.9s) was *above* the ~1.6s pre-ADR-0093 baseline the comment
   claimed to beat.
   **Fix:** `r = r.WithContext(ctx)` in the move handler (not just the
   local `ctx` variable) so every subsequent `renderHeldStrip(w, r)` /
   `renderHeldStripWithToast(w, r, ...)` call picks up the marker via
   `r.Context()`, plus adding the same `crossTillHotPathNetCtx` bound to
   `fetchTablesFromPrimary` itself. `GET /ui/held` (a genuinely
   single-hop, unmarked call to the same `renderHeldStrip`) is
   deliberately untouched and keeps its full 800ms.
2. **Should-fix, fixed: the resume hop count was wrong (5 claimed, 4
   real) — the same accounting error propagated through several
   comments.** `parkCurrentBasket`'s auto-park branch always ends in
   `d.Engine.Reset()`, which clears the engine's table id *before*
   `resumeHeldSale` reads `prevTable` — so the old-table-release call
   right after always sees an empty `tableID` and
   `releaseTableClaimWriteThrough`'s own `if tableID == "" { return
   false }` short-circuits before any network call. Auto-park and a real
   release are mutually exclusive on this branch; they never both cost a
   hop on the same request. Corrected to "up to four" (matching the
   issue's own original ~3.2s figure exactly) in
   `crossTillHotPathProxyTimeout`'s doc comment, `resumeHeldSale`'s own
   inline comment, and the resume test's doc comment / setup comment
   (the T2 busy-basket setup does NOT produce a release hop, contrary to
   what the first draft's comment claimed — kept for realism, comment
   corrected).
3. **Should-fix, fixed: the move test's timing assertion had ~5% (94ms)
   headroom** against its measured 1.906s pre-BLOCKER-1-fix value — not
   comfortably safe as a wall-clock assertion in a package that already
   takes 112s and can run under contention. Fixing BLOCKER 1 drops the
   real figure to 1.506s, and the threshold is now derived
   (`hopCount × crossTillHotPathProxyTimeout × 2`) rather than a bare
   literal, giving ~3s of headroom on a ~1.5s figure while still failing
   clearly (3.5s) against the true unbounded regression.
4. **Should-fix, fixed: the resume test never proved the local *delete*
   fallback landed**, only the local *read* fallback (via the restored
   engine snapshot). `resumeHeldSale` swallows
   `heldSaleDeleteWriteThrough`'s own error (`_ = err`) and still returns
   success, so a poisoned-context local delete would have passed this
   test silently. Added `SELECT COUNT(*) FROM held_sales WHERE id = ?`
   (expect 0) for the resumed order's own row.
5. **Nit, fixed: timing literals not derived from constants.** The
   700ms delay and the 2s/94ms-headroom threshold were bare literals that
   would silently change meaning if `crossTillHotPathProxyTimeout` or the
   client `Timeout`s ever moved. Extracted `hotPathTestPrimaryDelay` as a
   named constant, added an explicit invariant assertion
   (`crossTillHotPathProxyTimeout < delay < client.Timeout`) at the top
   of both tests, and derived the pass/fail threshold from the hop count
   instead of a bare `2*time.Second`.
6. **Nit, fixed (comment only): "dropping packets" language.** The fake
   primary is slow-but-responsive (a real `time.Sleep` before serving),
   not a true blackhole — client-side the two are indistinguishable
   (both time out the same way), so the test is fine in substance, but
   the comment now says so explicitly rather than implying the primary
   never answers at all (it does, which is why the un-fixed run's logs
   show the primary's own handler logging `context canceled` when the
   client gives up mid-request — expected noise, not a bug).
7. **Nit, accepted, not changed: helper duplication
   (`newSlowHoldCrossTillPrimary` vs. `newHoldCrossTillPrimary`) and
   `crossTillHotPathNetCtx`'s placement in `hold_api.go` rather than
   beside its consumers.** Both are real, minor style points; neither
   affects correctness or test integrity, and this card's scope is the
   latency fix, not a refactor of adjacent test/helper structure. Logged
   here rather than silently dropped; a future touch of this area is
   free to fold them in.
8. **Nit, accepted, not changed: shortening the budget slightly widens
   an existing, already-accepted ADR-0093 divergence window** (primary
   applies a write, but this till's own confirmation round-trip times
   out against a merely-busy — not actually unreachable — primary,
   leaving the local copy `primary_synced=0` a little more often than at
   800ms). Still bounded and self-healing on the next successful
   write-through for the same id, same as the pre-existing outage case.
   Documented as a stated trade-off in `crossTillHotPathProxyTimeout`'s
   own comment rather than left implicit.
9. **Process, addressed:** this review record (was missing at the time
   of the first review pass). No ADR contradiction — ADR-0093's own text
   was checked and its only latency figure (the ~800ms Open-orders merge
   budget) is untouched by this diff, so no ADR amendment is needed; the
   card's own "document the accepted trade-off" framing (issue option 3,
   partially adopted alongside option 2) is satisfied by this record plus
   the constant's own doc comment.

Verified independently by the reviewer, not taken on trust:

- **Exhaustive hop sweep**, not spot-checked: all local repo fallback
  calls across the three (now four) touched proxy files use the
  deadline-free outer `ctx`, never the derived `netCtx` — confirmed by
  file:line enumeration, not sampling.
- **Defer ordering** in every proxy helper: `cancel()` is deferred before
  `resp.Body.Close()`, so bodies are always fully decoded before the
  context is torn down.
- **No scope leak**: the marker is set exactly twice (resume, move), never
  mutates the original `*http.Request` object identity in a way that
  reaches other requests, and every other caller of the four proxy
  helpers (the live basket's table pick, a manual Hold, `GET /ui/held`
  on its own, the Open orders listing, the floor plan) is confirmed
  unaffected — still the full 800ms.
- **Claim-before-release ordering** in the move handler: confirmed
  byte-for-byte unchanged by this diff.
- **TDD re-verification, twice** — once by the reviewer (against the
  first draft, both tests), once independently after this session's own
  BLOCKER 1 fix (reverting *only* the `r = r.WithContext(ctx)` line,
  confirming `TestHeldTableMove_HotPathBoundedWhenPrimaryIsSlow` fails at
  3.512s with the exact predicted message, then restoring and confirming
  it passes again at ~1.5-2.6s across repeated runs).
- `guard-data-access.sh` reasoning re-confirmed: the new test's raw
  `INSERT`/`SELECT` SQL is in a `_test.go` file, which the guard's own
  `grep -v '_test.go'` exempts.

## Verified beyond automated tests

- Full gate: `gofmt -l .` (clean, repo-wide), `go build ./...`, `go vet
  ./...`, `go test ./...` (repo-wide, green), `golangci-lint run ./...`
  (0 issues, repo-wide).
- `scripts/ci/guard-data-access.sh` (pass).
- The two new timing tests re-run repeatedly (`-count=3` to `-count=8`,
  including under artificial CPU contention) to confirm the assertions
  are not flaky at their new, derived thresholds.

## Deferred / follow-up

- Test-helper duplication (`newSlowHoldCrossTillPrimary`) and
  `crossTillHotPathNetCtx`'s file placement — cosmetic, noted above, not
  filed as a separate card (too small to be worth one).
- If a future change adds another write-through/read hop reachable from
  either the resume or move handler, it needs the same
  `crossTillHotPathNetCtx` treatment explicitly — nothing here makes that
  automatic, and this card's own review is proof a hop can be missed on
  the first pass.

**Safe to merge.**
