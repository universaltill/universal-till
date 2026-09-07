# Code review — cross-till table-claim write-through (ut-docs#1703)

**Date:** 2026-09-07
**Branch:** `fix/1703-cross-till-table-claim-writethrough`
**Reviewer:** independent review pass (different model), worktree-isolated, did not
write the implementation under review.
**Verdict:** **safe to merge** after the two blocking fixes (and three smaller
ones) below were applied in this branch and pinned by tests.

---

## What shipped

Two tills sharing a primary could each claim the same `table_claims` row: the
claim/release write was purely local and was never proxied cross-till
(`table_claims` is excluded from the admin-bundle sync by name). ut-docs#1392
had already shipped the read-only occupancy *display* proxy; this is the write
half.

- **`internal/db/migrations/008_table_claims_till_id.sql`** — adds
  `till_id TEXT NOT NULL DEFAULT ''` to `table_claims`. `''` is THIS till's own
  local claim (the same this-till convention `sales.till_id` uses); any other
  value is the `tills.id` that took the claim through the sync API. No FK to
  `tills(id)`, deliberately, so a revoked till's claim stays readable and
  expirable rather than blocking `DeleteTill`.
- **`internal/data/tables_repo.go`** — `ClaimTableForTill` (reconcile-then-claim
  in one transaction) and `ReleaseTableClaimForTill` (delete scoped to the
  calling till).
- **`internal/pages/sync_tables_claim.go`** — primary-side bearer-authed
  `POST /api/sync/tables/claim` and `/release`, `syncTill`-authed, standard
  `{ "data": …, "error": null }` envelope. `tillClaimTTL = 2 * time.Minute`,
  deliberately the same bound `sync_admin.go`'s "is this till online" status
  chip uses, so the chip and a claim's expiry can never disagree.
- **`internal/pages/tables_claim_proxy.go`** — replica-side write-through
  wrapping `/api/pos/table` and the hold/resume re-claim. Primary's answer is
  authoritative when reachable; **any** failure falls back to exactly today's
  local-only behaviour (offline-first, ADR-0003).
- **`internal/auth/middleware.go`** — both new paths added to the exempt list.

---

## Findings

### 1. BLOCKER (fixed) — a till permanently locked a table against itself

`ClaimTableForTill` treated a re-claim by the till that already owns the row as
an ordinary "occupied" refusal (`INSERT OR IGNORE` → 0 rows → `claimed=false`),
and this was explicitly asserted by the original test suite.

That is not survivable, because the TTL only expires a claim whose owning till
has gone **quiet**:

1. Replica R picks table T. The primary records `T → R`.
2. R dies mid-basket (power cut), or R's release write-through hits an
   unreachable primary — the local row is dropped either way, by design; the
   primary's row is not.
3. R reboots. Its boot sweep clears only its own **local** rows. It starts
   talking to the primary again, so `tills.last_seen_at` is fresh, so R is
   "online", so the staleness rule can **never** expire R's own orphan.
4. The operator re-picks T on R and gets the "occupied" toast — forever. No
   other till can take T either (R is live). There is no in-product recovery
   until #1393 ships a manual free-the-table action.

This is a **regression**: before #1703 the boot sweep alone fully recovered
this case, and it defeats the card's own acceptance criterion that a till
crashing mid-claim must not lock a table past a bounded, documented TTL.

**Fix:** the reconcile `DELETE` now also drops the *calling* till's own row, so
re-claiming a table you already hold always succeeds (and refreshes
`claimed_at`, which is what the floor plan renders as "occupied since"). Safe
because there is exactly one live basket per till: a row recorded against a
till id *is* that till's single current pick, and this call is that till
replacing it with itself. The `till_id != ''` guard is kept, so the primary's
own local claim is still never touched, and a different **live** till is still
refused. Scoped to the same `table_id` deliberately — a blanket "drop all my
claims" would free the OLD table before the NEW claim is confirmed, inverting
`/api/pos/table`'s deliberate claim-new-then-release-old ordering.

Pinned by `TestClaimTableForTill_OwnOrphanedClaimIsRetakenAfterRestart`
(repo) and `TestSyncTablesClaim_OwnOrphanedClaimIsRetakenAfterRestart`
(full HTTP surface, which also re-proves the owner comes from the bearer).
Both include a negative half: a different live till is still refused.

### 2. BLOCKER (fixed) — the boot sweep wiped every replica's live claims

`ClearAllTableClaims` was `DELETE FROM table_claims`, unconditional. That was
correct while claims were local-only; with #1703 the primary's `table_claims`
also holds the live claims of **replicas that are still running**, with those
tables still on their screens. So every primary restart re-opened exactly the
cross-till double-claim this card closes: the replica keeps its table locally
while the primary now reads it free and hands it to the next till that asks.

The new code's own doc comment already asserted the invariant it did not
establish ("a till's boot-time ClearAllTableClaims only ever clears its OWN
local rows") — it did not.

**Fix:** renamed to `ClearLocalTableClaims` (a method called `ClearAll…` that
deliberately does not clear all of them is how the next bug gets written) and
scoped to `WHERE till_id = ''`. Replica-owned rows need no sweep here: TTL
reconciliation in `ClaimTableForTill` is what expires them, and only for a till
that has genuinely gone quiet. On a replica this is a no-op change — every row
it writes locally is a `''` row. Pinned by
`TestClearLocalTableClaims_LeavesAnotherTillsClaimAlone`.

I had been asked to consider this one as a possible follow-up card. It is not:
it is a direct consequence of the ownership column *this* card introduces, it
silently voids this card's own guarantee, and the fix is one scoped `WHERE`.

### 3. Medium (fixed) — the primary was the one till the TTL takeover did not apply to

Found by following through the consequences of fix 2 rather than in the diff as
written, and it is fix 2's own consequence, so it belongs in this card.

With replica rows now surviving on the primary, they can outlive the replica —
that is the design, and `ClaimTableForTill` expires them. But the **primary's
own** basket never calls `ClaimTableForTill`: it is not a replica, so
`claimTableWriteThrough` took its local branch, which was a plain
`ClaimTable` with no reconciliation. A replica that died holding a table would
therefore block the main till from that table indefinitely — nothing on that
path ever revisits another till's row, and (after fix 2, correctly) the boot
sweep no longer clears it. The main till would have been the one till in the
shop the documented ~2-minute takeover did not apply to, which would also have
made the manual wording added in finding 5 false.

**Fix:** the local branch now calls `ClaimTableForTill` with an **empty** till
id. That writes the same `''` row `ClaimTable` would have, and reconciles stale
rows the same way. The `''` id can only ever expire *another* till's stale row —
`ClaimTableForTill`'s `till_id != ''` guard keeps it off every local row,
including this till's own — so the primary's own claim semantics are unchanged.
On a replica (only `''` rows locally) and on a standalone till (no `tills` rows)
it reconciles nothing and is exactly the previous behaviour. Pinned by
`TestClaimTableWriteThrough_LocalBranchExpiresAnotherTillsStaleClaim`, which
checks both halves: a **live** till's claim still blocks the primary, a dead
one's does not.

**And a robustness point that fix caused, also fixed:** the local branch is the
one a till runs when nothing else is reachable, so it must not have become
easier to fail than the single `INSERT` it replaced. It now degrades to the
plain `ClaimTable` if the reconciling form errors for any reason, so a failure
there costs the reconciliation bonus and never the pick itself (offline-first,
ADR-0003). Pinned by
`TestClaimTableWriteThrough_LocalBranchStillClaimsWhenReconcileFails`.

The existing hold/resume tests caught this immediately — their hand-rolled
schema has no `tills` table, so the reconciling call errored and resume's
re-claim failed. That is a test-fixture gap, not a production one (`tills` is in
`001_init.sql`), and it is exactly the trap finding 4 predicted; the fixture now
creates `tills` as well, so those tests exercise the real path rather than
silently passing through the degraded fallback.

### 4. Low (fixed) — `hold_api_test.go`'s hand-rolled schema had drifted

The hand-written `CREATE TABLE table_claims` lacked `till_id`. Genuinely
harmless today (the hold/resume path only reaches `ClaimTable` /
`ReleaseTableClaim`, neither of which names the column), so those tests were
not silently broken — but a hand-rolled schema drifting from the real one is a
trap for the next change that does touch it. Column added.

### 5. Medium (fixed) — the manual did not describe the new behaviour

CLAUDE.md: the user manual ships with the feature. This card changes what an
operator sees — a table can now be refused because a *different till* holds it,
and a crashed till's table frees itself after about two minutes. `tables.md`
still described reservation in single-till terms ("a second order can't pick
it"). Added a bullet covering cross-till reservation, the offline fallback, and
the ~2-minute recovery, in **all four** shipped locales (en/fa/ar/tr, matching
the convention of recent help-prose commits), and regenerated `make docs-shots`.

### 6. Accepted as-is — write-through latency on basket actions

A blackholed primary costs up to 800 ms per proxied call, and `/api/pos/table`'s
move path makes two sequentially (claim new, release old) — so up to ~1.6 s on a
table move, and up to 800 ms on tender/reset while the release is posted.

Not a violation of the offline-first rule: nothing is *blocked*, the sale still
completes, and `completeTender` posts the release only after the sale is
recorded and published. The 800 ms budget is the same one the sibling read
proxy (`tables_sync_proxy.go`) already uses on page render, and is deliberately
shorter than `order_status.go`'s 3 s. Accepted. If floor feedback ever shows
this, the release — which is already documented and tested as fire-and-forget —
is the piece to move to a background context; that is a follow-up, not this
card.

### 7. Accepted as-is (follow-up worth a card) — a restarted till's orphan on a
table it never revisits

With finding 1 fixed, a restarted till reclaims its own table the moment the
operator picks it again, which is the overwhelmingly common recovery path. The
residual: if the till restarts and *never* touches that table again while
staying online, its orphan still blocks other tills, because the TTL keys on
till liveness rather than claim liveness. A complete fix needs the replica to
tell the primary "release everything of mine" at boot (a boot-time
write-through, or a claim generation/epoch) — real new surface, and a network
call during `Init` deserves its own design. **Recommended as a follow-up card**;
bounded meanwhile by #1393's planned manual free-the-table action. I did not
invent that design during review.

### 8. Dismissed — not real issues

- **Raw SQL outside `internal/data` / `internal/db`.** None.
  `sync_tables_claim.go` inlines no query; `guard-data-access.sh` passes, and I
  read the files rather than trusting the guard.
- **Authorization hole (claiming/releasing as another till).** No. Both handlers
  derive the owner from `syncTill`'s bearer resolution (`till.ID`); `table_id`
  is the only client-supplied input, trimmed, rejected empty (400), and further
  constrained by the `REFERENCES tables(id)` FK. There is no code path that
  reads a till id from the request.
- **`till_id` accidentally `''` for a real till.** No. `TillsRepo.InsertTill`
  assigns `uuid.NewString()`, and `TillByBearerHash` returns that stored id;
  there is no path producing an empty till id. An unexpirable "permanent" claim
  is therefore unreachable for a replica.
- **TOCTOU between reconcile and claim.** No. Both statements run inside one
  `BeginTx`; the `DELETE` takes SQLite's write lock, so a concurrent claim
  cannot interleave between the expiry and the `INSERT OR IGNORE`, and
  `table_claims.table_id` is the PRIMARY KEY, making the insert itself the
  race-free primitive.
- **`NOT IN` with NULLs.** Correctly guarded: the subquery filters
  `last_seen_at IS NOT NULL` and selects the non-nullable `id`, so no NULL can
  poison the `NOT IN`.
- **Timestamp comparison.** `tills.last_seen_at` is written by
  `TillByBearerHash` as `time.RFC3339` UTC, the exact format and zone the cutoff
  is formatted with, so the lexicographic `>=` is sound.
- **Migration numbering.** `008` follows `007` with no gap or collision
  (`guard-migration-version-collision.sh` passes), and
  `ADD COLUMN … NOT NULL DEFAULT ''` is additive and backward-compatible — no
  data loss, no NOT NULL violation against existing rows.
- **New i18n key needed.** No. The cross-till rejection falls into the same
  `!claimed` branch in `/api/pos/table` that already renders
  `basket.table.occupied`; the key exists in `en.json` and `guard-i18n.sh`
  passes. Claim verified, not taken on trust.
- **Hardcoded demo shop name / secret-shaped literal.** None. The only fixed
  strings added are test fixtures (`"Till 2"`, `"bearer-t2"`, `"hash-"+id`) in
  `_test.go` files.
- **Stale `migration 077` / `078` references** in pre-existing comments (the
  table actually lives in `001_init.sql` after the ADR-0074 squash). Pre-dates
  this card, cosmetic, left alone to keep the diff honest.

---

## What I personally verified

Ran in this worktree, not taken from the implementation report.

**Gate (clean):** `gofmt -l .` (no output), `go build ./...`, `go vet ./...`,
`go test ./...` (whole repo, all packages pass), `golangci-lint run ./...`
(0 issues), and **every** `scripts/ci/*.sh` guard referenced by
`.github/workflows/ci.yml` — all pass except `guard-deadcode-baseline.sh`,
which fails in this sandbox only because the GTK/WebKit pkg-config packages are
absent (`pkg-config --exists gtk+-3.0` → 1), breaking `internal/thirdparty/webview_go`.
Environmental, identical on an untouched tree, unrelated to this diff.
`make docs-shots` regenerated and `guard-docs-shots.sh` green.

**Break-then-confirm-the-test-fails exercises.** Each mutation was applied,
the named test run and observed to fail with a real assertion (not a compile
error), then reverted and confirmed green:

1. Dropped `AND till_id = ?` from `ReleaseTableClaimForTill` (delete by
   `table_id` alone) → `TestReleaseTableClaimForTill_OnlyDeletesOwnClaim` fails:
   *"another till's release must not touch the claim, got \"\" (ok=false)"*.
2. Removed the stale-expiry `DELETE` entirely →
   `TestClaimTableForTill_StaleOwnerIsExpiredAndTakenOver` fails:
   *"takeover of a stale till's claim: claimed=false, want true/nil"*.
3. *(my own choice)* Removed the `till_id != ''` guard →
   `TestClaimTableForTill_NeverExpiresThisTillLocalClaim` fails:
   *"a till_id='' (this-till-local) claim must never be expired"*.
4. *(my own choice)* Dropped `/api/sync/tables/release` only from the auth
   exempt list, leaving `/claim` → `TestSyncPullPathsAreExempt` fails naming
   exactly `/api/sync/tables/release`, proving the test pins **both** paths
   independently rather than one standing in for the other.
5. *(my own choice)* Made `claimTableOnPrimary` treat a `{"data":null}` 200 as
   `ok=true, claimed=false` instead of a failure →
   `TestClaimTableOnPrimary_Contract` fails. The fallback matrix the card
   claims — not-a-replica, connection failure, non-200, malformed JSON, 200
   with null data — is genuinely covered; timeout shares the `client.Do` error
   path with connection failure.

**My own fixes were verified the same way**, in reverse: I reverted both
production fixes and confirmed all four new/changed assertions fail against the
unfixed code —
`TestClaimTableForTill_OwnOrphanedClaimIsRetakenAfterRestart`,
`TestSyncTablesClaim_OwnOrphanedClaimIsRetakenAfterRestart`,
`TestClearLocalTableClaims_LeavesAnotherTillsClaimAlone`, and the amended
owner-re-claim assertion in
`TestClaimTableForTill_FreshClaimSucceeds_SecondTillRefusedWhileOwnerFresh` —
then restored the fixes and confirmed green. Before fixing finding 1 I also
reproduced it standalone against the unmodified branch. Finding 3's fix was
checked the same way: reverting the local branch to the plain `ClaimTable`
makes `TestClaimTableWriteThrough_LocalBranchExpiresAnotherTillsStaleClaim`
fail on *"the primary must be able to take a dead till's table"*.

Two things the tests caught on me rather than the other way round, both worth
recording because they are the reason the fixes are shaped as they are:

- The first draft of finding 3's test seeded the other till with
  `InsertTill`, which leaves `last_seen_at` NULL — already stale by the TTL
  rule — so the "a live till still blocks the primary" half passed vacuously.
  It failed loudly instead of passing quietly, and the test now sets
  `last_seen_at` explicitly.
- Finding 3's first fix broke `TestHoldThenResume_MovesTableClaimBetweenLiveAndHeld`
  and `TestResume_ReleasesEmptyBasketsPriorTableClaim`: their hand-rolled
  schema has no `tills` table, so the reconciling call errored and resume's
  re-claim failed outright. Not a production defect (`tills` is in
  `001_init.sql`), but it made the point that the offline-critical local branch
  had gained a new way to fail — which is why that branch now degrades to the
  plain `ClaimTable` on any reconcile error, and why the fixture creates `tills`
  so those tests exercise the real path rather than the fallback.

**Read in full, not skimmed:** the migration, both new repo methods,
`sync_tables_claim.go`, `tables_claim_proxy.go`, the `pos_api.go` /
`hold_api.go` / `init.go` call-site diffs, the `middleware.go` diff and
`exempt()`'s matching semantics (a `switch path` — exact match, so both entries
were genuinely required), plus `TillsRepo` and `replicaSyncTarget` to trace
where a till id actually comes from.

---

## Safe to merge

Yes. The design is sound and mirrors its stated precedents
(`fetchOrdersFromPrimary` / `registerSyncOrders`) closely; the offline-first
fallback is real and well covered; auth derives the owner from the bearer
throughout; the reconcile-then-claim is genuinely atomic. The blocking
correctness bugs found are fixed in this branch with regression tests that were
demonstrated to fail without the fixes. Finding 7 is recommended as a follow-up
card and is not a merge blocker.
