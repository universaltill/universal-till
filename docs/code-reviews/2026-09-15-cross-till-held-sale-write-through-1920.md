# Code review — cross-till held-sale (open order) write-through sync (ut-docs#1920)

- **Date:** 2026-09-15
- **Ticket:** ut-docs#1920 (`complexity:hard`, split c/3 of ut-docs#1903)
- **Branch:** `feat/1920-held-sale-cross-till-sync`
- **Design:** ADR-0093, including Amendment A (`ut-docs/adr/0093-cross-till-held-sale-write-through-sync.md`)
- **Reviewer:** two independent rounds, Opus (different model from the Fable
  implementation), per `complexity:hard` routing. Round 2 was earned by
  round 1 finding blocker-class (money-loss) issues; both rounds ran with
  real build/test/guard execution and genuine revert-then-restore TDD
  verification, not a read-only diff pass.
- **Verdict: SAFE TO MERGE.** Four blocker-class findings across two review
  rounds, all fixed and re-verified with fail-first tests in this same
  cycle. Two should-fix items (clock skew, sub-second resolution) are
  explicitly deferred — see below.

## What shipped

Held sales (parked/open orders) were previously invisible cross-till: a
restaurant with two tills couldn't add to, or even see, an order parked
on the other one — table *occupancy* already crossed tills via
`table_claims`' write-through, but the order's actual contents never did
(product-owner feedback, ut-docs#1903, 2026-09-09).

- Migration `internal/db/migrations/030_held_sales_updated_at.sql` adds
  `updated_at` and `primary_synced` to `held_sales` and
  `held_sales_archive`.
- `internal/data/held_sales_repo.go`: `UpsertIfNewer` is a
  predicate-guarded `INSERT ... ON CONFLICT DO UPDATE ... WHERE
  held_sales.updated_at <= excluded.updated_at` (same idiom as
  `DebitVoucherForRedemption`, ADR-0084) — a stale concurrent write
  refuses cleanly (`applied=false`) instead of clobbering a newer one.
  `ReconcileWithPrimary` marks ids the primary confirmed and drops local
  mirrors the primary has since resolved.
- `internal/pages/sync_held_sales.go`: primary-only, bearer-authed
  `POST /api/sync/held-sales/upsert`\|`/delete`, `GET /api/sync/held-sales`.
- `internal/pages/held_sale_sync_proxy.go`: `heldSaleWriteThrough`/
  `heldSaleDeleteWriteThrough` (replica write-through, mirrors
  `claimTableWriteThrough`'s shape — ANY failure reaching the primary
  degrades to local-only, silently, offline-first); `heldSaleForResume`
  (primary-first lookup, shared by resume and the table-move handler);
  `mergeHeldSales` (the Open orders page's primary-wins merge).
- `internal/pages/hold_api.go`: park/re-park/resume/table-move all route
  through the write-through instead of touching the repo directly.
- `internal/pages/open_orders_page.go`: merges local + a best-effort live
  primary list, reconciling stale mirrors on every successful fetch.
- Help docs (`web/help/{en,de,tr,ar,fa}/open-orders.md`) updated — the
  "each till shows only its own held orders" claim was no longer true;
  `make docs-shots` re-run twice (after the original diff and again after
  the review-fix round), zero rendered pixels changed either time
  (confirmed: no `.png` under `web/help/img/` appears in `git status`
  either run, only `manifest.json`'s surface hash).

## What the independent review found and fixed

**Round 1** (against the first implementation):
- **F1 (blocker):** `heldSaleForResume` read the local mirror first,
  primary only as a not-found fallback — backwards from the Open orders
  page's own primary-wins ordering. Proven end-to-end: the page correctly
  showed till A's newer 2-line addition, but resuming on till B restored
  the stale 1-line local payload, silently dropping the item and
  undercharging the sale.
- **F2 (blocker, money):** after till A resumed-and-cashed-out an order,
  till B's local mirror of that same row was never cleaned up — till B
  could resume and re-ring an already-paid-for order.
- **F3 (should-fix):** `POST /api/pos/held/table` bypassed the
  write-through entirely (a local-only `UPDATE`, never reaching the
  primary, never bumping `updated_at`).

Fixed via ADR-0093 Amendment A: a `primary_synced` marker lets a mirror
tell "never confirmed on the primary" (outage-taken, keep) apart from
"confirmed, then absent from a later primary list" (resolved elsewhere,
drop); `heldSaleForResume` now asks the primary first; the table-move
handler routes through the write-through.

**Round 2** (scoped re-review of the Amendment A fix, earned by round 1's
blocker findings):
- **F10 (blocker, money — introduced by the F3 fix):** the table-move
  handler built its write-through payload from `repo.Get`'s *local* row,
  not the primary-first lookup — so a move could push this till's stale
  payload/line_count/total_minor onto the primary, silently erasing
  another till's added items. Proven end-to-end (primary went from 2
  lines/240 to 1 line/120 after a move). Fixed: the handler now calls
  `heldSaleForResume` (the same primary-first lookup the resume path
  uses) instead of `repo.Get`.
- **F11 (blocker, money):** the primary's predicate-refusal branch in
  `heldSaleWriteThrough` left the local row `primary_synced=false` — but
  a refusal is itself proof the primary holds a newer row for that id, so
  the row was indistinguishable from a genuinely outage-taken one and
  survived reconciliation forever, re-opening F2's ghost-resume on that
  specific path. One-line fix: `h.PrimarySynced = true` before the
  fallback `repo.Upsert` on that branch.

Both re-verified with new fail-first tests
(`TestHeldTableMoveOnReplica_DoesNotClobberPrimaryNewerPayload`,
`TestHeldSaleWriteThrough_RefusalStillMarksPrimarySynced`) — confirmed
failing with real assertion errors against the pre-fix code (via
`git stash`), passing after.

- **F12 (informational, round 2):** a narrow, non-monetary TOCTOU between
  a reconcile's list-snapshot and a concurrent write-through's mirror on
  the *same* till — worst case, a just-created row drops off that till's
  local-only view until its next write-through for that id; the row
  itself is never lost (still on the primary, still reachable via the
  merge and primary-first resume). Not fixed this cycle; noted here for
  the record.

## Verified beyond automated tests

- Full `go build ./...`, `go vet ./...`, `go test ./...` (all packages)
  run independently by this reviewer twice — once after the Amendment A
  fix, once after the F10/F11 fix — both green, not just trusted from the
  implementer's report.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh` all green
  independently, `make docs-shots` actually executed (Playwright/Chromium
  available in this pipeline session) rather than deferred.
- TDD claims re-verified by genuine revert-then-restore on the single
  most safety-critical assertion in each round: the stale-upsert refusal
  (round 1) and both round-2 fixes (F10/F11) — each confirmed to fail
  with a real assertion error pre-fix and pass post-fix, not merely
  "tests were added."
- Offline-first traced explicitly on every new/changed code path: every
  primary call (upsert, delete, list, and the resume/move lookups built
  on them) degrades to local-only on not-a-replica, network error,
  timeout, non-200, or malformed body — resume and move never block or
  fail when the primary is unreachable.
- No real client/shop name, no literal credential, anywhere in the diff
  (migration, tests, or help docs).

## CI finding, fixed post-review

The `deadcode-baseline` guard (whole-program `deadcode` under the `desktop`
build tag) flagged two functions the F10/F11 fix left with no remaining
production caller: `HeldSalesRepo.Insert` (superseded by `Upsert`, which
was already insert-or-update-safe for a fresh id — see `Upsert`'s own doc
comment) and `HeldSalesRepo.SetTable` (superseded by routing the table-move
handler through `heldSaleForResume`/`heldSaleWriteThrough`, per F10 above).
Per the guard's own stated intent (ut-docs#1566: the baseline is meant to
shrink, not grow), both were removed rather than added to the baseline;
their dedicated unit tests were adapted to exercise `Upsert` directly
(`TestHeldSalesRepo_TableID`) or renamed where they only ever tested
`Upsert`'s own already-existing behaviour under the old name
(`TestHeldSalesRepo_UpsertGetListDelete`). No behavior change — full
`go build`/`go vet`/`go test ./...` re-verified green after.

## Explicitly deferred (accepted, not blockers)

- **Clock-skew / sub-second resolution** (ADR-0093 Amendment A's own
  F4/F5): the predicate guard compares each replica's own wall-clock
  `updated_at` (second resolution) rather than a primary-assigned
  monotonic version. A skewed till's writes can be durably refused, and
  two writes inside the same one-second window both apply. Neither is a
  new failure mode this card introduces (no cross-till write existed
  before it), and a real fix needs a bigger primitive (primary-assigned
  version) than this cycle's scope — tracked as a new Backlog card rather
  than ballooning this diff further.
- **F12** above.
- A full per-line/N-way merge for two simultaneous edits to the *same*
  order (ADR-0093's own original non-goal) — last-writer-wins-with-clean-
  refusal only, matching the depth ADR-0084 shipped for vouchers.
