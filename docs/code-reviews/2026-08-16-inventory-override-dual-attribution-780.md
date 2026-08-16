# Code review: negative-inventory override drops the blocked actor's identity (ut-docs#780)

**Date:** 2026-08-16
**Author (build):** Sonnet (inline, complexity:easy)
**Reviewer:** fresh-context Sonnet subagent, independent (isolated worktree, no prior context of the build)

## What changed

`CreateNegativeInventoryOverride` (`internal/pages/inventory_api.go`) already
had an ad-hoc PIN-based manager-override flow: a blocked cashier supplies a
manager's PIN, the manager becomes the audit actor. The original (blocked)
cashier's identity was never recorded anywhere — the audit row read as if the
manager performed the action directly, unlike `fiscal_api.go`'s TSE override,
which already captures both identities via a `requested_by` field.

This change follows that existing precedent:

- `internal/pages/inventory_api.go` — captures `requestedBy :=
  getSessionUserID(r)` before the role/PIN branch (mirrors
  `createTSEOverride`'s `requestedBy`); `actorID` stays the mutable "who
  becomes the audit actor" value, reassigned only on the PIN-approval path.
  Both are now threaded into `pos.OverrideNegativeInventory{...}`.
- `internal/data/pos_repo.go` — adds a `RequestedBy` field to
  `OverrideNegativeInventory`; `RecordNegativeInventoryOverride` adds a
  `requested_by` key to the audit payload only when it differs from
  `ActorID` (same conditional-inclusion convention `fiscal_api.go` uses,
  just placed in the repo layer next to the `INSERT` rather than in the
  handler — same observable behavior, arguably a cleaner spot since the
  payload shaping is already there).
- Regression tests added at both layers: `internal/pages/inventory_api_test.go`
  (`TestCreateNegativeInventoryOverride_CashierRequiresManagerPIN` now also
  asserts `requested_by="cashier1"` in the audit payload; a new
  `TestCreateNegativeInventoryOverride_AdminSelfApproves_NoRequestedBy`
  covers the complementary case — no `requested_by` key when the actor
  self-authorizes) and `internal/data/pos_repo_batch8_inventory_test.go`
  (`TestRecordNegativeInventoryOverride_RequestedBy_Batch8`, direct repo-layer
  coverage of both branches).

No SQL query text added outside `internal/data` — the handler still calls
through `pos.RecordNegativeInventoryOverride`; only the payload JSON built
inside the existing `INSERT INTO audit_log` gained one more conditional key.

## Verification

- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` (full suite) — all packages pass.
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all green.

## Independent review

A fresh-context Sonnet subagent, isolated in its own git worktree (no
visibility into the build reasoning), reviewed the diff cold:

- Confirmed `requestedBy`/`actorID` are never confused across the
  self-authorized vs. PIN-approved branches, and both always reach the audit
  write.
- Compared directly against `fiscal_api.go`'s `createTSEOverride` and found
  the shape consistent (same capture-before-branch pattern); the only
  difference — where the conditional `requested_by` inclusion lives (handler
  vs. repo layer) — produces identical observable behavior, not a real
  divergence.
- Re-verified the data-access rule: no SQL text in `internal/pages`, single
  `INSERT` site in `internal/data/pos_repo.go`.
- **Independently re-ran the TDD claim**, not just read it: reverted
  `internal/pages/inventory_api.go` and `internal/data/pos_repo.go` to their
  pre-fix (`main`) versions while keeping the new tests, confirmed
  `internal/data`'s new test fails to even compile (`unknown field
  RequestedBy`) and `internal/pages`'s
  `TestCreateNegativeInventoryOverride_CashierRequiresManagerPIN` fails with
  `expected requested_by="cashier1" ... got ""`; restored the fix and
  confirmed both pass again, with the full package tests, build, and vet all
  clean afterward.
- Checked for any other call site constructing `OverrideNegativeInventory`
  that should also pass `RequestedBy` — none found; the handler is the only
  production caller.
- No real client/shop name used in test data.

**Finding raised (non-blocking, fixed before merge):** the `RequestedBy`
field's doc comment said it would be "empty" when the actor self-authorizes;
in practice the handler always sets it equal to `actorID` in that case (the
repo-layer guard condition already handles both "empty" and "equal" the same
way, so there was no functional bug — just a slightly misleading comment for
a future reader). Reworded to say "equal to ActorID" instead of "empty."

**Verdict: safe to merge**, no blocking findings.

## Scope notes

- Non-goal (per the issue): retrofitting `inventory_api.go`'s bespoke PIN
  flow onto ut-docs#557's generic elevation mechanism once that lands —
  this ticket is the standalone minimal fix (matching `fiscal_api.go`'s
  existing convention), not blocked on #557.

Closes universaltill/ut-docs#780.
