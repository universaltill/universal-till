# Code review — LAN-sync journal replay: EnsurePaymentMethod before payment insert (ut-docs#1681)

**Date:** 2026-09-06
**Author (Dev):** Sonnet, inline (complexity:medium)
**Reviewer:** Opus, fresh-context subagent, isolated worktree
**Verdict:** PASS — no blockers. Two should-fix findings, both fixed before merge.

## What changed

`applyJournal` (`internal/pages/sync_sales.go`), the LAN-sync journal-replay
path a primary uses to apply a replica's offline sales, never called
`repo.EnsurePaymentMethod` before inserting a payment — unlike the live
tender path (`pos_api.go`) and refund path (`refund_page.go`), which both
already do. `payments.method_id` has a real FK to `payment_methods(id)`, so
a replica sale tendered with a payment method not yet in the primary's
`payment_methods` table (e.g. a voucher tender) failed journal replay with
a raw, uncaught FOREIGN KEY violation instead of replaying.

Fix: call `repo.EnsurePaymentMethod(ctx, p.Method)` in the payment-building
loop before appending to `in.Payments`, guarded on non-empty method,
returning the error (retryable batch-reject, matching `EnsureStockLocation`'s
handling a few lines above) rather than quarantining.

Two new regression tests in `internal/pages/sync_sales_payment_method_fk_test.go`,
using a real migrated DB (`internal/db.Open`) rather than this package's
existing hand-rolled `openPagesTestDB`/`seedForPages` fixture — deliberately
not touching that shared fixture since ut-docs#1676 is a separate, in-flight
refactor of it, and the FK doesn't exist in the old hand-rolled fixture
(which is exactly why this bug was invisible before #1676's work).

## TDD verification (re-verified for real, not by reading)

Reviewer reverted only the production fix (kept the new test), ran it,
confirmed it failed with the exact reported failure mode:

```
insert payment: constraint failed: FOREIGN KEY constraint failed (787)
```

Restored the fix, confirmed it passed again. Same revert/restore done a
second time for the should-fix S1 guard (below) against its own test.

## Findings

### Should-fix (both fixed before merge)

**S1 — an empty `p.Method` would upsert a junk `payment_methods` row.**
The first draft copied `pos_api.go`'s `EnsurePaymentMethod` call but not
the guard that protects it (`pos_api.go` skips the whole payment on an
empty method *before* calling `EnsurePaymentMethod`, which itself doesn't
validate its id). Proven empirically: an unguarded call on
`{"method": "", "amount": 500}` still gets rejected downstream by
`pos.CompleteSale` (batch-reject-and-retry, same as before), but leaves
behind a blank, nameless `payment_methods` row that sorts into the
cashier's live tender list — a real, if low-likelihood (replica-authenticated
peer only), regression. Fixed: guard the ensure call on `p.Method != ""`;
this changes nothing about `CompleteSale`'s own rejection, only removes the
side effect. Pinned by a new test,
`TestApplyJournal_EmptyPaymentMethodDoesNotPolluteLivePaymentMethods`,
which fails without the guard (`found 1` junk row) and passes with it.

**S2 — the journal contract doc didn't describe this replay behaviour.**
`ut-docs/reference/contracts/pos-lan-sync-journal.md`'s `payments[].method`
bullet had zero replay semantics documented, while every neighbouring field
(`voucher_id`, `currency`, `created_at`) documented its behaviour in detail.
Fixed: updated the bullet inline and added a 1.6.0 changelog row (separate
ut-docs commit/PR, same session — `docs/1681-journal-contract-payment-method`).

### Not fixed — considered and rejected (with reasoning)

**Centralizing `EnsurePaymentMethod` inside `CompleteSale`/`InsertPayment`**
(raised as an open question in the issue's own "Suggested direction"): the
reviewer's recommendation, adopted here, is that the current shape — ensure
where the caller *knows* free-text methods are legitimate (cashier tender,
refund, journal replay), validate instead where they aren't (the anonymous
self-order kiosk) — is the correct design, not an accident to fix:

1. It would silently remove a deliberate security boundary:
   `self_order_shop.go`'s payment path carries an explicit comment that it
   must NOT fall back to `EnsurePaymentMethod` (that surface is anonymous
   and auth-exempt), and instead validates against
   `ListActiveNonCashPaymentMethods`. Centralizing the ensure into
   `CompleteSale` would remove the FK backstop behind that gate without
   touching the gate itself — nothing would fail today, but the invariant
   the comment states would become false at the persistence layer.
2. It would make a money-adjacent write (`EnsurePaymentMethod` hardcodes
   `type='cash'`) implicit on every sale path, widening its blast radius
   for no benefit this card needs.
3. `CompleteSale` runs inside a transaction; `EnsurePaymentMethod` does not
   — centralizing is a real refactor (thread the tx through, or accept a
   non-transactional write inside a transactional operation), not a
   tidy-up, and out of scope for a p1 bug fix.

**Classifying `EnsurePaymentMethod` failures as quarantinable** (rather than
batch-reject-and-retry): rejected — `permanentJournalFailureReason`'s own
contract is "would fail identically forever if retried unchanged."
`EnsurePaymentMethod`'s failure modes (locked DB, disk full, closed handle)
are environmental, not a property of the entry's content; quarantining one
would permanently and silently drop a valid sale's replication the moment a
disk is briefly full. `return false, "", err` (batch-reject-and-retry)
matches the sibling `EnsureStockLocation` call's handling a few lines above.

### Follow-up filed, out of scope here

Two more FK gaps on the identical replay path, same signature, both proven
empirically during review:

| Gap | FK | Observed error |
|---|---|---|
| Replica-local cashier | `sales.cashier_id → users(id)` | `insert sale: ... FOREIGN KEY constraint failed (787)` |
| Replica-local item | `sale_lines.item_id → items(id)` | `insert sale lines batch: ... FOREIGN KEY constraint failed (787)` |

Both are raw, uncaught, not on the quarantine allowlist — each would wedge
a replica's entire subsequent replication forever, the identical failure
mode this card fixes. The right shape is probably not N more `Ensure*`
calls but a generic "classify a raw SQLite FK violation (787) as
quarantinable" branch in `permanentJournalFailureReason`, matching
ADR-0065's stated intent. Filed as ut-docs#1686.

### Nits (no action needed)

- `EnsurePaymentMethod` hardcoding `type='cash'` means a replayed
  card/voucher-typed method reads as cash-typed on the primary. No impact
  today (`applyJournal` never sets `RegisterID`, and the only `pm.type`
  consumers are register-scoped or fail-safe on a mis-typed method) —
  pre-existing, not introduced here, worth knowing if replayed sales ever
  gain a register.
- The ensure runs outside `CompleteSale`'s transaction, so the method row
  can survive a subsequently-rejected/quarantined entry — same as the
  `pos_api.go` precedent, acceptable.

## Verification (local gate, both before and after the S1/S2 fixes)

- `gofmt -l .` — clean
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./internal/pages/... ./internal/pos/... ./internal/data/...` — all green
- `go test ./...` (full suite) — all green
- `golangci-lint run ./internal/pages/...` — 0 issues
- `scripts/ci/guard-data-access.sh` — passes (test file's raw SQL is in a
  `_test.go` file, exempt by the guard's own scope)
- All other CI-blocking guards in `ci.yml`'s `build` job run locally — pass
  (unaffected: no new user-facing strings, templates, routes, or self-order
  surfaces touched)

## Merge

`merge_method: "merge"` (never squash/rebase — preserves real commit
attribution, ut-docs#250).
