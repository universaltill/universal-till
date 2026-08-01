# GoBD immutability/completeness assessment — audit log & financial records

Verification pass for ut-docs#40 ("Verify audit log meets GoBD immutability
requirements"). GoBD (Germany's *Grundsätze zur ordnungsmäßigen Führung und
Aufbewahrung von Büchern*) requires financial records to be traceable,
complete, correct, timely, orderly, and — the focus here — **immutable**:
once recorded, a financial record must not be alterable or deletable within
its retention period (10 years) without leaving a trace. This is part of the
German fiscal-compliance surface tracked alongside ut-docs#38 (TSE signing)
and ut-docs#39 (DSFinV-K export); those two are the certified-signing/export
half, this is the "can existing data actually be destroyed" half.

## What was checked

Grepped every write path against `audit_log` and every `UPDATE`/`DELETE`
against `sales` and its child tables in `internal/data/pos_repo.go` and
`internal/data/plugin_repo.go` (the only two files with SQL access to these
tables — enforced by `scripts/ci/guard-data-access.sh`).

## Finding 1 — `audit_log` itself is append-only in application code (PASS)

Every write to `audit_log` is an `INSERT` — the named helper methods
(`PluginRepo.InsertAudit`, `PluginRepo.InsertAuditRaw`, `POSRepo.InsertAudit`
— `internal/data/plugin_repo.go:119,133`, `internal/data/pos_repo.go:1709`)
plus two inline sites in the stock-movement path
(`internal/data/pos_repo.go:444,486`). No `UPDATE audit_log` or
`DELETE FROM audit_log` exists anywhere in the codebase. The table has no
`updated_at`/version column, consistent with an insert-only design.

**Caveat (not a code bug, a hardware-signing gap already tracked):**
`audit_log` has no cryptographic tamper-evidence — no hash chain linking
each row to the previous one, no external signing. From an
application-code threat model this doesn't matter (there is no code path
that could tamper with it), but it means immutability today rests entirely
on "no code path does this," not on a verifiable property of the data
itself. Real tamper-evidence is what a certified TSE (ut-docs#38) would
add — this finding doesn't need separate tracking, it's the same gap #38
already owns.

## Finding 2 — normal sale lifecycle preserves history correctly (PASS)

Voiding a sale (`POSRepo.UpdateSaleStatus`, `internal/data/pos_repo.go:2060`)
sets `status`/`voided_at` on the existing row — it does not delete or
overwrite the original sale content. `CleanupObsoleteItems`
(`internal/data/pos_repo.go:2016`) explicitly excludes any item or variant
that appears in `sale_lines` or `stock_movements` from deletion (see the
`obsoleteItemsWhere` predicate, `internal/data/pos_repo.go:1971`) — items
with real financial/stock history are deactivated, never removed. Both are
the correct GoBD-compatible pattern: history stays, only current-state flags
change.

## Finding 3 — `ResetTransactionHistory` can irrevocably destroy real
financial records, with no code-level guard (REAL GAP)

`POSRepo.ResetTransactionHistory` (`internal/data/pos_repo.go:1849`),
reachable via the manager-gated `POST /api/data/reset-transactions` handler
(`internal/pages/data_api.go:25`), permanently `DELETE`s every row in
`invoices`, `sale_links`, `payments`, `sale_discounts`, `sale_lines`,
`sales`, `held_sales`, `shifts`, `stock_movements`, and `report_archive` —
i.e. every financial and till-operational record the product has. It writes
one summary row to `audit_log` (`"transaction_history_reset"`, a sales-count
only) — the underlying records themselves are gone with no way to recover
them; not voided, not archived, actually deleted.

The intent is legitimate and already documented: the handler's own comment
and the UI copy (`web/locales/en.json:590`, "This permanently deletes ALL
sales, shifts and invoices... it is a one-time reset before going live")
frame this as a pre-launch, wipe-the-demo-data tool, gated behind manager
role + a typed `RESET` confirmation. That's a reasonable feature to have.
(That role gate itself is weaker than it sounds: `isManagerOrAuthOff`,
`internal/pages/settings_page.go:34-40`, returns `true` unconditionally
when `UT_AUTH=off` — with auth disabled there is no role check at all on
this destructive endpoint, only the typed confirmation string.)

**The gap:** nothing in the code enforces the "before going live" part.
There is no check for shop-claim status, sale age, sale count, or any other
signal that would distinguish "wiping demo data before a shop's first real
sale" from "a manager wiping ten years of a live shop's legally-retained
sales history on day 400." Today this is pure honor system — a manager
(or anyone with a manager PIN, or anyone at all with `UT_AUTH=off`) can
invoke it at any point in a shop's lifetime, and the result is permanent,
unrecoverable, and satisfies none of GoBD's retention requirements for
whatever it deleted.

Two signals already exist in-tree that the eventual gate could build on,
without inventing a new concept: `enroll.Status.Registered`
(`internal/enroll/enroll.go:107`, today's marketplace/device-registration
state) and the shop-profile step in **proposed** ADR-0026 (setup wizard +
eager registration), which would establish a clearer pre-launch/live
boundary than registration alone. Neither is a ready-made answer — the
policy call is still #187's to make — but they're the natural starting
points rather than a from-scratch signal.

This is currently a latent risk, not a live one — no real shop is onboarded
yet ("Task Runner" is test data), so nothing has actually been destroyed
that shouldn't have been. But it needs a code-level gate before the first
real shop goes live, and *what that gate should be* is a product decision
(what signal distinguishes "pre-launch" from "live"?), not something to
guess at here. Filed as **ut-docs#187** (`status:triage`, `p2`,
`compliance`, `needs-info`) with the specific question and a recommended
default, same shape as ut-docs#14's "pre-first-onboarding" framing.

## Scope not covered here

- Certified transaction-signing (TSE/KassenSichV) and DSFinV-K export are
  separately tracked (#38, #39) and depend on an architectural decision
  (ADR-0025) this assessment doesn't make.
- This assessment is code-only; it doesn't cover operational controls
  (who holds manager PINs, backup handling) — those are process, not this
  codebase's concern.

## Verdict

`audit_log` and the normal sale/void/cleanup lifecycle already satisfy
GoBD's immutability expectation. The one real gap is
`ResetTransactionHistory` having no code-level guard against use on a live
shop — tracked as its own card (#187) pending a product decision on the
exact gate, not fixed in this pass.
