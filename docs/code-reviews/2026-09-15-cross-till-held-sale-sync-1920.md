# Cross-till held-sale write-through sync (ut-docs#1920, ADR-0093)

**Date:** 2026-09-15
**Card:** ut-docs#1920 (split c/3 of ut-docs#1903)
**Builder:** Fable subagent (this cycle, `lane:cloud-41`, `complexity:hard`)
**Reviewer:** Opus subagent, fresh context, independent of the build (per
`complexity:hard` routing — see `scrum-master`'s "Model routing by
complexity"); findings applied and re-verified by the orchestrator (Sonnet)

## What shipped

`held_sales` (a parked/open order) previously lived on exactly one till —
`hold_api.go`'s own comment on the table-claim write-through noted plainly
that "held_sales itself still isn't synced or proxied to the primary at
all." This closes that gap: an order parked on till A is now visible, and
resumable/addable, from any other till in the shop.

- Migration `030_held_sales_updated_at.sql`: `updated_at` on `held_sales`
  (+ its `held_sales_archive` twin), the cross-till ordering key.
- `HeldSalesRepo.UpsertIfNewer`: primary-side idempotent upsert guarded by
  an `updated_at`-based `WHERE` predicate on `ON CONFLICT ... DO UPDATE`,
  mirroring ADR-0084's `balance >= ?` guard shape for vouchers.
- Three new bearer-authed primary endpoints (`sync_held_sales.go`):
  `GET /api/sync/held-sales`, `POST .../upsert`, `POST .../delete`. Wired
  into `internal/auth/middleware.go`'s exempt list, pinned by
  `TestSyncPullPathsAreExempt` in both directions (exact-match, not a
  prefix).
- Replica write-through (`held_sale_sync_proxy.go`), mirroring the
  existing `claimTableWriteThrough`/`voucherRedeemWriteThrough` shape:
  primary authoritative when reachable, silent local-only fallback on any
  failure — offline-first unchanged.
- Open-orders page merge (`open_orders_page.go`): folds the primary's live
  list into the local one for display.
- Full design record: ADR-0093 (`ut-docs/adr/0093-...md`, PR
  universaltill/ut-docs#2267, amended in #2269 per the findings below).

## Review process

Two passes, per the card's `complexity:hard` routing:

1. **Fresh-context Sonnet** reviewed the ADR document itself before
   implementation (see `ut-docs/code-reviews/2026-09-15-adr-0093-*.md`) —
   found and fixed a clock-skew framing gap in the original design.
2. **Opus, independent, fresh context** reviewed the actual implementation
   diff against the ADR, the repository's own conventions, and this
   codebase's sibling precedents. It independently re-ran the full
   `internal/pages` suite three times (once under deliberate concurrent
   load) to chase down a one-off test failure the orchestrator had seen
   earlier and lost the detail of (see "Flake investigation" below), and
   concretely verified two load-bearing tests actually fail when the
   behavior they claim to test is broken (not just read-and-trust).

## Findings and fixes

### BLOCKER — mirror-on-read created permanent ghost rows (fixed)

The first implementation draft mirrored every primary row that won the
open-orders merge into the replica's own `held_sales`, on every page
render, with nothing to ever remove it. Concretely: till A parks order X
on table T5; till B opens `/open-orders` and X gets mirrored into B's
local `held_sales` with `table_id = T5`; till A resumes and tenders X. B's
mirrored row for X survives indefinitely, because the merge only adds and
B's own delete write-through only fires on B's *own* resume.

Consequences, both reachable through local SQL with no idea the row was
foreign: `POSRepo.IsTableFree`/`ListTablesWithState` both count local
`held_sales` rows, so **T5 reads permanently occupied on B** even after
A's order is long since paid; and X keeps rendering as resumable on B —
tapping it would resume, and could re-tender, an **already-paid sale**.
This also broke precedent: every other cross-till read merge in this
codebase (`tablesWithStateForDisplay`, `fetchOrdersFromPrimary`) is
display-only and never persists.

**Fix:** `mergeHeldSalesWithPrimary` is now a pure display transform — no
repository write. Cross-till resumability is handled separately, on
demand: `resumeHeldSale`'s local-miss path now calls
`resumeHeldSaleWithPrimaryFallback`, which checks the primary directly and
mirrors the row locally only at the moment this till is genuinely about to
hold it (and its own resume then deletes it shop-wide immediately after,
same as any other order). ADR-0093 Decision 4 amended to match (PR
universaltill/ut-docs#2269). New/updated tests:
`TestOpenOrdersOnReplica_MergesPrimaryRowsWithoutMirroringLocally` (renamed
from `...AndMirrorsThem`, assertions inverted to prove the render writes
neither side), `TestResumeHeldSaleFallback_FetchesFromPrimaryOnLocalMiss`,
`TestResumeHeldSaleFallback_NotFoundLocallyOrOnPrimaryStaysNotFound`.

### SHOULD-FIX — `updated_at` guard's real semantics deviated from the ADR (fixed via ADR amendment)

The implementation always blanks `updated_at` on the wire and lets the
*primary* stamp it — every stamp the guard ever compares comes from one
clock, so the guard can never be defeated by inter-till clock skew. The
ADR as originally written specified a replica-stamped value and explicitly
*accepted* clock skew as a residual risk. The code is objectively better
(it closes the residual rather than bounding it), so rather than weakening
the code to match the document, ADR-0093 Decision 2 was amended to
describe primary-side stamping and retract the now-inapplicable
clock-skew paragraph (universaltill/ut-docs#2269), per ADR-0007
document-first — a document divergence from shipped behavior doesn't get
to stand uncorrected.

### SHOULD-FIX — delete write-through was cancelable by the request that triggered it (fixed)

`heldSaleDeleteWriteThrough` used the raw request context for its primary
call, unlike its own doc comment's cited precedent
(`releaseVoucherOnPrimary`), which detaches via
`context.WithTimeout(context.WithoutCancel(ctx), ...)` specifically so a
cashier navigating away mid-request can't cancel an in-flight primary
call. A fast double-tap or navigation could cancel the delete and strand
exactly the un-self-healing orphan the function's own comment names as
the one outcome that doesn't self-heal. Fixed to match the cited
precedent exactly.

### SHOULD-FIX — archive round-trip silently dropped the new column (fixed)

`held_sales_archive` (the reset/restore twin of `held_sales`) had no
`updated_at` column, and `reset_archive_repo.go`'s `resetArchiveTables`
cols string for `held_sales` didn't carry it — exactly the drift class
this file's own comments warn about four times over (055
`held_sales_archive.table_id`, 056 `tracking_token`, 007 `local_date`, 013
`display_no`). Nothing failed today (no test pinned it), but a go-live
reset+restore would have silently reset every restored parked order's
ordering key to `''`. Fixed: migration 030 also adds the column to
`held_sales_archive`; `resetArchiveTables`'s cols string updated; new
regression test `TestResetThenRestoreRoundTrip_HeldSalesUpdatedAt`
(`internal/data/reset_test.go`) — **verified failing without the fix**
(`archived held_sales updated_at = "", want "2026-09-15 09:30:00"`) and
passing with it, before landing.

### Accepted as follow-up, not fixed this cycle

- **Serial 800ms write-through hops can stack on the resume/move hot
  paths** (up to ~3.2s worst case on a blackholed-not-refused primary, up
  from ~2.4s before this card). Real, but a conscious latency-budget
  question rather than a correctness bug, and out of this card's scope to
  redesign the call sequencing. Filed as a follow-up Backlog card
  (ut-docs#2270) rather than guessed at here.
- **The sale-screen's parked-orders popup / held strip stay local-only**
  (not merged with the primary) — this matches ADR-0093's own stated
  non-goal ("no new UI beyond wiring the existing open-orders page"), so
  left as-is, not a gap.
- Minor: `postHeldSaleOnPrimary` doesn't drain the response body on a
  non-200/malformed path before closing it — matches every existing
  sibling proxy in this file exactly (same pattern, same omission), so
  left consistent with precedent rather than fixed in isolation.

### Flake investigation

A `go test -count=1 ./internal/pages/...` run during this review's Tester
pass failed once (package-level `FAIL`, 241s) with the specific failing
test lost to a `tail -40` truncation. Five further full non-cached runs of
the same package (two by the orchestrator, three by the independent Opus
reviewer, one deliberately overlapped with concurrent `internal/data`/
`internal/db` runs to add load) all passed cleanly, zero `--- FAIL` lines.
The reviewer traced through both new proxy test files line by line: no
`time.Sleep` calls, `httptest` servers respond effectively instantly, and
the "unreachable" fallback cases use an already-closed port (immediate
connection-refused), leaving no plausible timing race against the 800ms
client budget. Conclusion: a one-off environmental flake unrelated to this
diff (pre-existing async/SSE test files in the same package are far more
plausible candidates), not a defect in the new code — noted here rather
than silently dropped, per this pipeline's own evidence-in-the-record
standard.

## Verification (independently re-run, not just trusted from the build)

- `go build ./...`, `gofmt -l .`, `go vet ./internal/pages/... ./internal/data/... ./internal/auth/... ./internal/db/...` — all clean.
- `golangci-lint run` on touched packages — 0 issues.
- `bash scripts/ci/guard-data-access.sh` — clean (no raw SQL outside `internal/data`/`internal/db`).
- `bash scripts/ci/guard-i18n.sh` — clean (no new user-facing strings; nothing to add).
- `go test -count=1 ./internal/pages/... ./internal/data/... ./internal/auth/... ./internal/db/...` — all green, run twice after the fixes above (once immediately after, once as the final pre-push gate).
- `TestResetThenRestoreRoundTrip_HeldSalesUpdatedAt` specifically confirmed to fail without its fix and pass with it (TDD discipline, not just asserted).
- No UI/markup changed (backend sync only — the open-orders page's existing template is unmodified, only the data feeding it); no new visual surface to screenshot-check.

## Non-goals honoured

No change to `table_claims` sync; no N-way/per-line merge (last-writer-wins
via the guard only); no new user-facing strings; no new page.

---
_Generated by [Claude Code](https://claude.ai/code)_
