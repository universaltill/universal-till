# Code review: diagnostics event-volume + correlation-id scoping (ut-docs#2234)

- **Date**: 2026-09-20
- **Branch**: `fix/2234-table-assignment-reaffirm-dedup`
- **Design**: `ut-docs` `adr/0092-persistent-diagnostic-mode-capture-and-live-tail.md`,
  "Amendment (2026-09-20)"

## What shipped

Two gaps found while scoping `internal/diagnostics`'s emitters (which
shipped with ADR-0092 §2, ut-docs#2169):

1. **`PluginAsk.CorrelationID` doc-comment only** (`internal/diagnostics/events.go`):
   records the decision to leave the correlation id unthreaded across a
   `pos.recomputeTotals` optimistic retry — threading a stable id through
   `pos.TaxRateAsker`'s interface was judged not worth the risk inside
   `internal/pos/service.go`'s sanctioned-exception locking territory
   (ut-docs#1317), for a diagnostics-only benefit. No functional/struct
   change.
2. **`TableAssignment` periodic-reaffirm dedup** (`internal/pages/tables_claim_proxy.go`):
   `claimTableWriteThrough`/`emitTableClaim` gain a `periodic bool`
   parameter. Every operator-driven claim (`pos_api.go`, `hold_api.go` ×2)
   passes `false` and keeps emitting unconditionally, exactly as before.
   Only `reaffirmHeldOrderTableClaims` (the ~30s held-order reaffirm tick,
   plus `Init`'s one-shot boot reaffirm) passes `true`: a new
   `periodicClaimEmitMu`-guarded, per-table cache keyed on
   `(diagnostic session id, outcome, via)` suppresses a `TableAssignment`
   emission only when all three are unchanged since the path's own last
   emission for that table. The underlying claim/DB work always runs
   regardless — only the diagnostic side effect is gated.

## Independent review

Fresh-context **Sonnet** subagent, worktree-isolated
(`isolation: "worktree"`, shared checkout never mutated), reviewing the
diff cold with no access to the implementation reasoning.

Model note: this card is `complexity:hard` (Dev→Fable, Review→Opus per
`MODEL-ROUTING.md`). Fable was out of usage credits (first Dev attempt
failed with `rate_limit` before making any change — confirmed clean
working tree, nothing to salvage), so Dev fell back to Opus per
`MODEL-ROUTING.md`'s own explicit fallback rule. Reviewing Opus's work
with Opus again would repeat the exact anti-pattern that rule exists to
avoid ("reviewing \[X\]'s work with \[X\] ... shares the author's blind
spots"), so Review ran on Sonnet instead — the documented "different
instance, fresh context" fallback shape MODEL-ROUTING.md names for the
`easy` tier, borrowed here since neither of `hard`'s own two models was
safely available for a genuinely independent second opinion.

**Verdict: SAFE TO MERGE AS-IS.** No blocking findings.

- Independently re-ran `go build ./...`, `go vet ./...`, `gofmt -l`, the
  targeted package tests, `-race` on the new/changed tests, and a full
  `go test ./...` — all green, matching what Dev/Tester already reported.
- Independently re-verified the TDD claim: neutralized the dedup gate in
  `emitTableClaim`, confirmed `TestPeriodicReaffirmSuppressesUnchangedTableAssignment`
  (and two other periodic tests) fail with the exact expected message
  (`table_assignment events = 2, want 1`), restored the code, confirmed
  green again.
- Confirmed no boot/ticker concurrency race is possible in production
  (`Init`'s boot reaffirm runs synchronously before the ticker starts;
  the ticker itself never fires an immediate first tick) — the mutex
  guard is still correct defensive practice regardless.
- Confirmed every non-test caller of `claimTableWriteThrough`/
  `emitTableClaim` repo-wide was updated correctly (grep, not just the
  touched files): 4 call sites, `reaffirmHeldOrderTableClaims` → `true`,
  every operator path → `false`.
- Confirmed the claim work itself is never gated, only the diagnostic
  event.
- Confirmed the new tests assert exact, specific behavior (counts,
  outcome/via values) rather than something that would pass with the fix
  broken.
- Confirmed no money, no SQL outside `internal/data`, no user-facing
  string, no secret-shaped literal, no real shop/client name in test data.

**Non-blocking observation** (not fixed, not worth a follow-up card):
`periodicClaimEmitLast` never evicts an entry for a table id that's since
been deleted/recreated (table ids are fresh UUIDs). In practice this is
inconsequential — entries are three short strings, the cache only grows
while a diagnostic session is active, and a till process restarts
routinely (updates/reboots), which zeroes the package-level map.

## What was verified beyond automated tests

- A one-off, additional real gap was found and fixed during this same
  session's own verification pass (before the independent-review subagent
  ran): the dedup key as first implemented compared only `(session id,
  outcome)`, which would have silently swallowed a genuine primary↔local
  failover (a real reachability transition) while the claim outcome
  stayed `claimed`. Fixed to key on `(session id, outcome, via)` instead,
  TDD-verified red-then-green with a new test
  (`TestPeriodicReaffirmEmitsWhenViaChanges`) — confirmed failing without
  the `via` comparison (`table_assignment events = 1, want 2`) and
  passing with it restored.
- Full `go test ./...` (whole repo, not just the touched packages) green,
  zero `FAIL` lines.

## Deferred / out of scope

Nothing deferred — both gaps named in ut-docs#2234 are fully addressed
(one as a documented accepted limitation, one as a code fix).
