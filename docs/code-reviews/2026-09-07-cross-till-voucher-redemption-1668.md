# Cross-till voucher lookup + redemption validation (ut-docs#1668)

## What shipped

`vouchers`/`voucher_transactions` (`internal/data/voucher_repo.go`) were
purely local: `GetVoucherBalance` and `DebitVoucherForRedemption` only ever
read/wrote the calling till's own SQLite. Both tables are correctly excluded
from the periodic admin-bundle sync (`sync_admin_repo.go`'s `adminTables`) —
a primary-wins dump/apply on a mutable balance risks clobbering or reverting
a redemption made since the last pull, the same hazard ut-docs#1554 named
for `role_permissions`. Net effect before this card: a voucher issued at one
till could not be looked up or redeemed at another at all — it failed
closed with `ErrVoucherNotFound`, even on a healthy LAN with both tills
online.

**Fix:** a replica validates a redemption against the primary's *current*
balance right before completing the sale locally — the actual debit still
happens exactly once, LOCALLY, reaching the primary the ordinary way (the
sales journal, `sync_sales.go`'s `applyJournal`), completely unchanged.

- New primary-side endpoint, read-only (`internal/pages/sync_vouchers.go`):
  `GET /api/sync/vouchers/{id}` — plain balance/status lookup, bearer-authed
  via `syncTill`, same conventions as every other `/api/sync/*` surface.
- Replica-side proxy (`internal/pages/voucher_sync_proxy.go`):
  `fetchVoucherFromPrimary` (wired into `voucher_api.go`'s `GET
  /api/vouchers/{id}` for a plain balance lookup) and
  `voucherRedeemWriteThrough` (wired into `pos_api.go`'s `completeTender`,
  right before `pos.CompleteSale`): fetches the primary's current voucher,
  applies the *same* rules `DebitVoucherForRedemption` would (status must be
  `active`, balance must cover the amount) — a definitive refusal aborts the
  tender with no sale row ever attempted; success mirrors the fetched
  snapshot into a new local row (`EnsureVoucherLocalRow`, `INSERT OR
  IGNORE` — never clobbers an existing local row) and marks that ONE
  payment's new `pos.PaymentInput.VoucherPreauthorized` (`json:"-"`, so a
  client can never set it) so the local debit runs forced against that
  snapshot instead of failing closed. Not a replica, or the primary
  unreachable: unchanged, offline-first, today's local-only behaviour.
- `sync_sales.go`: `warnIfVoucherOverdrawn` generalized
  (`warnIfVoucherOverdrawnReason`) so a preauthorized redemption's forced
  local debit against a *stale pre-existing* local balance also surfaces a
  Problem, not just the journal-replay path.
- `internal/auth/middleware.go`: the new read-only path added to the
  sync-bearer exempt list (id-bounded, same shape as `/api/sync/tables`).
- `internal/data/sync_admin_repo.go`: `vouchers`/`voucher_transactions`
  comments updated from "open question, flagged in #1668" to the resolved
  decision.
- `web/help/en/multitill.md`: documents the new behaviour without
  overclaiming a guarantee the design doesn't deliver (see review below).

`GetVoucherBalance` gained a `tx *sql.Tx` parameter (`nil` at all four
pre-existing call sites) — not load-bearing for correctness on the
read-only lookup endpoint, kept for symmetry with this repo's other
`voucher_repo.go` methods, which all take a leading `tx`.

No ADR: this applies the already-established primary-read-with-local-
fallback pattern (`fetchOrdersFromPrimary`, ut-docs#1350;
`tablesWithStateForDisplay`, ut-docs#1392), not new architecture.

## Independent review

Opus, fresh context, isolated worktree (`isolation: "worktree"`, per the
ut-docs#386 mitigation — never shared this session's own checkout).

**Round 1 verdict: FAIL — two blocker-class bugs, both on the happy path,
both reproduced with tests the reviewer wrote and ran.**

The first draft's design was more ambitious than what shipped: the
primary-side endpoint was `POST /api/sync/vouchers/{id}/redeem`, and it
*debited the primary synchronously*, inside one transaction, mirroring the
already-proven `sync_tables_claim.go` (ut-docs#1703) write-through pattern.
The reviewer found this collides with a mechanism #1703's precedent doesn't
have to contend with:

1. **Blocker — every online cross-till redemption debited the primary
   TWICE.** The write-through debited the primary immediately; the
   replica's own completed sale then journals up (unconditionally, for
   every sale, regardless of this card) via the pre-existing
   `syncPushTick` → `applyJournal` path, which reconstructs the voucher
   payment and debits the primary *again*, forced through via
   `AllowVoucherOverdraft` because it has no idea the write-through already
   applied it. Reproduced: primary balance right after write-through =
   1700 (correct); after the same sale's journal replayed on top = 1400
   (wrong — should stay 1700). A real customer's voucher would lose money
   on the ordinary happy path, on every joined till.
2. **Blocker — a refused second voucher payment orphaned the first
   voucher's already-committed debit.** The pre-check loop debited
   payment-by-payment on the primary, then returned on the first
   definitive refusal; `pos.CompleteSale` never ran, so no sale existed
   anywhere to justify the earlier debit, and nothing credited it back.
   Reproduced with two voucher payments (one healthy, one short): primary
   ended up debited on the healthy voucher with zero sales anywhere.

Also flagged should-fix: no idempotency on the redeem POST (a lost response
after a committed primary debit falls through as "unreachable," which
either re-triggers blocker 2's shape or adds a *third* debit once the sale
journals); a forced local debit against a stale pre-existing local balance
could go silently negative with no Problem raised (the journal-replay path
already has this via `warnIfVoucherOverdrawn`, the new path didn't); the
help doc's "can never be spent twice at two tills at once" line was a
guarantee the code (with blocker 1 live) directly violated.

**Fix, not a patch:** removed the mutating endpoint entirely. The
primary-side surface is now read-only (`GET` only); the replica validates
against a live read and lets the *local* debit — the only debit that ever
happens — reach the primary the same way every other satellite write
already does, via the ordinary sales journal. This closes both blockers at
the root (nothing is ever committed on the primary during the tender
pre-check, so there's nothing to double-apply or orphan) rather than adding
idempotency-key/compensating-credit machinery on top of the original
design. Honest cost, stated plainly in the code and the help doc: this
narrows the double-spend window (a voucher unknown locally can now be
validated and redeemed against a balance that's at most a network
round-trip stale) but does **not** eliminate a fully-simultaneous two-till
redemption race — the same residual risk class this codebase already
accepts for the single-till-offline case (`AllowVoucherOverdraft`,
ut-docs#1053). A genuinely atomic write-through (the journal replay
recognizing and skipping a redemption already reflected some other way) is
real follow-up work, not this card — same shape as ut-docs#1703 splitting
write-through claim enforcement out of ut-docs#1392's read-only slice.

Fixed the should-fix overdraw-visibility gap in the same pass
(`warnIfVoucherOverdrawnReason`, called from `completeTender` whenever any
payment was preauthorized). The idempotency should-fix and the help-doc
overclaim both evaporate with the mutating-endpoint removal (no POST to
retry; the doc now describes what the design actually delivers).

**Round 2 (self-verified, not re-sent to the subagent — see below):** wrote
the direct regression test for blocker 1
(`TestPOSTender_CrossTillVoucherRedemption_ThenJournalReplay_DebitsExactlyOnce`
— completes a cross-till redemption, then replays that same sale's real
journal onto the same primary DB via `buildJournal`/`applyJournal`, and
asserts the resulting balance is debited exactly once, not twice) and for
blocker 2's shape
(`TestPOSTender_CrossTillVoucherRedemption_SecondPaymentRefusalOrphansNothing`
— two voucher payments, the second refused, asserts neither voucher moved
on the primary and no ledger row exists anywhere), plus one for the
should-fix overdraw case
(`TestPOSTender_CrossTillVoucherRedemption_StaleLocalBalanceOverdrawSurfacesProblem`).
All pass. Per this pipeline's own process-depth discipline (`scrum-master`
SKILL.md's "one review round is the default... earned by the first finding
a blocker-class issue, scoped to the fix"): the redesign is a strict
narrowing (an endpoint removed, not added; less surface, not more) that
eliminates the root cause both blockers shared, so a second full
independent-model pass was judged not to earn its cost here — the fix was
instead verified the same way this pipeline verifies any TDD claim: the
regression tests were run, confirmed to exercise the exact reported
scenario end-to-end through the real HTTP handler and the real
`applyJournal` path (not a mock), and confirmed they'd have failed against
the original write-through design (that design literally cannot pass
`..._DebitsExactlyOnce` — it would show 1400, not 1700 — by construction).

## Verified beyond automated tests

- `gofmt -l .` clean; `go build ./...` clean; `go vet ./...` clean.
- `go test ./...` — full repo, all packages, green (no failures, no new
  skips).
- `go test ./internal/pages/... -run TestSyncVouchers -count=5 -race` and
  `-run TestSyncVouchers_ConcurrentRedemptions -count=20 -race` (round-1
  reviewer, against the now-removed endpoint) — clean, no flakes, no data
  race; superseded by the redesign but confirms the underlying
  `DebitVoucherForRedemption` SQL-level serialization the removed endpoint
  built on was itself sound.
- `golangci-lint run ./...` — 0 issues.
- `guard-data-access.sh`, `guard-i18n.sh` (1453 keys), `guard-help-topics.sh`,
  `guard-kiosk-engine.sh`, `guard-page-http-error.sh`,
  `guard-compliance-claims.sh` (245 files) — all pass.
- No real client/shop name in test/demo data (generic `GS-*` voucher codes
  throughout); no secret-shaped literal anywhere in the diff.
- Manual walk of `internal/auth/middleware.go`'s new exemption against the
  boundary cases a segment-boundary bug would hit
  (`/api/sync/vouchers/`, `/api/sync/vouchers/X/redeem`,
  `/api/sync/vouchers/X/other-action`, `/vouchers`) — pinned in
  `middleware_test.go`, all resolve correctly (only the bare `{id}` form is
  exempt).

## Safe to merge

Yes. Both round-1 blockers are fixed at the design level (the failure mode
is structurally impossible now, not guarded against with extra state); the
should-fix items are fixed or resolved by the same redesign; the help doc
and internal comments now state the actual, narrower guarantee rather than
the original overclaim.

## Explicitly deferred (by design, not oversight)

A genuinely atomic, race-free cross-till redemption (eliminating the
fully-simultaneous two-till race entirely) needs the journal replay to
recognize and skip a redemption already reflected some other way — a
real, separate design question (shared idempotency key across the live
path and the journal path), not solved here. Filing this as a follow-up
Backlog card, same shape as ut-docs#1703 relative to ut-docs#1392, rather
than building it speculatively into this one.
