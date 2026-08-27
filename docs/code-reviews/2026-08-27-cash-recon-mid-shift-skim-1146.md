# Code review: mid-shift skim breaks the printed CASH RECONCILIATION identity (ut-docs#1146)

**Date:** 2026-08-27
**Card:** ut-docs#1146
**Repo/area:** `universal-till` — `internal/data/pos_repo.go` (`CashReconciliationForLocalDay`), `internal/pos/shifts.go` (`CloseShift`), plus test coverage in `internal/data/pos_repo_cash_recon_test.go`.

## What shipped

`CashReconciliationForLocalDay` bucketed **every** `cash_adjustment` audit
row with `type='skim'` into the printed report's `Skim` field, regardless
of when it was recorded. That's correct for the only skim path the shipped
operator UI offers — a skim entered as part of closing a shift
(`pos.CloseShift`'s own skim-to-safe field) — because `expected_cash` is
computed and persisted *before* that close-time skim audit row is written,
so it never enters `Calculated`, matching its exclusion from the printed
sum:

```
OpeningFloat + CashSales + TipsHeldOut + PayIns + PayOuts == Calculated
```

But `pos.RecordCashAdjustment` also accepts `Type:"skim"` on a still-open
shift (not reachable via the shipped UI, but valid at the API layer, and
deliberately so per the pre-existing `TestRecordCashAdjustment_SkimType`).
A mid-shift skim like that already sits in `audit_log` by close time, so
`SumShiftAdjustments`/`ComputeExpectedCash` nets it into `Calculated` the
same as any other mid-shift adjustment — but the reconciliation query
still excluded it from the visible sum, breaking the identity by exactly
the mid-shift skim amount.

**Fix:** `pos.CloseShift` now stamps an explicit `at_close: true` marker
into the skim audit row it writes at close. `CashReconciliationForLocalDay`
only counts a `type='skim'` row toward `Skim` when that flag is present;
every other skim row (including a mid-shift one, which never carries the
flag) falls through to the ordinary sign-based split and lands in
`PayIns`/`PayOuts`, keeping the identity whole regardless of timing.

Non-goal honored: no UI changes — the shipped mid-shift adjustment form
still doesn't offer "skim" (`shifts.html`), so nothing a shop owner sees
changed. `web/help/en/reports.md`'s existing prose about the identity and
about skim being entered at close only got *more* accurate, not stale, so
no help-doc edit was owed.

## Independent review

Spawned as an isolated-worktree Opus subagent (this is a `complexity:medium`
card — Sonnet built it, Opus reviewed it, per the pipeline's model-routing
rule). It ran the real build/vet/test/guard commands rather than trusting
the diff, and did its own adversarial pass on the core claim.

**First pass verdict:** SAFE TO MERGE, with one **Medium** finding (F1) and
one **Low** finding (F2). Both fixed before this record was written; the
fixes are reflected in the diff this record covers, not left as follow-up.

### F1 (Medium, fixed) — `created_at == closed_at` is not a reliable
discriminator at second precision

The original fix identified a close-time skim by comparing the skim row's
`created_at` to the shift's `closed_at` (both stamped from `pos.CloseShift`'s
same `now` variable, in the same transaction — a real invariant for the
happy path). The reviewer pointed out `time.RFC3339` is only
**second-precision**, and reproduced — through real production code paths,
not a hand-crafted fixture — a mid-shift skim recorded via
`RecordCashAdjustment` in the exact same wall-clock second as its own
shift's close: its `created_at` then coincidentally equals `closed_at`,
misclassifying it as the close-time skim and reproducing the #1146 bug
verbatim.

**Fix:** replaced the timestamp comparison with the explicit `at_close`
flag described above. `pos.RecordCashAdjustment` never sets it, so a
mid-shift skim is unambiguous regardless of timing — same second or not.
Added `TestCashReconciliationForLocalDay_MidShiftSkimSameSecondAsClose`,
which reproduces the reviewer's exact same-second scenario and asserts it
now correctly lands in `PayOuts`, not `Skim`.

This intentionally does **not** keep a timestamp-based fallback for a skim
row written before this flag existed (which would reopen the exact
ambiguity it closes for future rows). Accepted deliberately: there is no
real production data yet to protect (this pipeline's standing "no real
users yet" auto-push authorization, 2026-07-29) — the only rows in any
existing DB are what this pipeline's own tests write, and those have been
updated to set the flag on every close-time-skim fixture. If that ever
stops being true, a schema migration/backfill would be the right fix, not
a probabilistic timestamp guess.

### F2 (Low, fixed) — physically impossible fixture

`TestCashReconciliationForLocalDay_MidShiftSkimIncludedInPayOuts`'s
`UpdateShiftClose` call passed a `new_float` of 10000 against a counted
cash of 7000 with no close-time skim — `pos.CloseShift` always computes
`new_float = closing_cash - skim`, so `new_float > closing_cash` can never
happen for real. Didn't affect any assertion (the test doesn't check
`NewFloat`), but weakened the fixture's claim to mirror a real call
sequence. Fixed to pass the same value (7000) for both, matching a real
close with no skim.

### F3 (info, accepted as documented) — the printed `Skim` line is now
narrower by design

A mid-shift skim genuinely is cash moved to the safe, but after this fix
it prints under "Pay-outs" rather than "Skim to safe" on the Z-report. This
is the correct trade — identity-over-label-fidelity, and the shipped UI
can't produce this case today anyway — but it's a real semantic narrowing
of what the `Skim` field means, now documented explicitly in
`CashReconciliation`'s doc comment. Rejecting `Type:"skim"` on an open
shift outright (the card's other proposed option) would have avoided the
ambiguity entirely, but `TestRecordCashAdjustment_SkimType` deliberately
asserts that path is allowed, so narrowing the printed field instead of
rejecting the API call is the smaller, more defensible change.

### Checks that came back clean (reviewer's own verification, not just a read)

- **NULL/missing `$.type` handling** — verified empirically with a seeded
  no-`type` adjustment plus a `type:"payout"` row: unaffected by this
  change, same split as before.
- **Cross-shift timestamp collision** — a different shift's mid-shift skim
  landing on another shift's `closed_at` doesn't leak across shifts; the
  `JOIN shifts s ON s.id = a.entity_id` scopes it per-shift. (Moot now that
  the fix no longer compares timestamps at all, but confirmed regardless.)
- **Other writers of `type:'skim'` rows** — exhaustive grep across
  `internal/`, `scripts/`, `e2e/`, `cmd/`: only `pos.CloseShift` and
  `pos.RecordCashAdjustment` ever write one.
- **Repository pattern / ADR-0040 / non-goal "no UI changes"** — all clean;
  diff touches only `internal/data/pos_repo.go`,
  `internal/data/pos_repo_cash_recon_test.go`, and (after the F1 fix)
  `internal/pos/shifts.go` — nothing under `web/`.
- **Timezone safety of the new tests** — derive the day via
  `b8ExpectedDay`'s `date(?,'localtime')` control query, not a Go literal,
  per the ut-docs#559 convention.

## TDD re-verification (independent, not taken on trust)

The reviewer reverted just the `pos_repo.go` SQL/comment change (keeping
the new tests) and re-ran them — genuine red:

```
--- FAIL: TestCashReconciliationForLocalDay_MidShiftSkimIncludedInPayOuts
    Skim: want 0 (no close-time skim on this shift), got -3000
    PayOuts: want -3000 (the mid-shift skim), got 0
    reconciliation identity broken: ... = 10000, want Calculated 7000
--- FAIL: TestCashReconciliationForLocalDay_MidShiftAndCloseTimeSkimBothPresent
    (analogous failure)
```

Restored the fix, both passed. I independently re-ran the full suite
myself after landing the F1/F2 fixes (below), rather than relying on the
reviewer's pre-fix run.

## Verification beyond the reviewer's pass

After applying the F1/F2 fixes, re-ran myself:

- `gofmt -l internal/data/pos_repo.go internal/data/pos_repo_cash_recon_test.go internal/pos/shifts.go` — empty.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` — all packages pass, including the full `internal/data`,
  `internal/pos`, and `internal/pages` suites (shift/skim/EOD coverage).
- CI-blocking guards touching this area: `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh` — all pass (this diff has
  no user-facing strings and no SQL outside `internal/data`).
- Targeted re-run of every cash-reconciliation test, including the new
  `TestCashReconciliationForLocalDay_MidShiftSkimSameSecondAsClose` added
  for F1 — all pass.

## Acceptance criteria (ut-docs#1146)

- [x] A mid-shift skim no longer breaks the printed reconciliation
      identity, including in the same-second edge case F1 found.
- [x] Regression tests: `TestCashReconciliationForLocalDay_MidShiftSkimIncludedInPayOuts`,
      `TestCashReconciliationForLocalDay_MidShiftAndCloseTimeSkimBothPresent`,
      `TestCashReconciliationForLocalDay_MidShiftSkimSameSecondAsClose`.
- [x] Doc comments on `CashReconciliation.Skim`/the struct-level identity
      comment corrected to describe the actual (flag-based) mechanism.

## Safe-to-merge verdict

**Yes.** Independent review found one real (Medium) gap in the first
draft's approach and one fixture defect; both are fixed and re-verified
in the diff this record describes, with a dedicated regression test for
the exact scenario the reviewer used to demonstrate the gap. No blocking
items remain. F3 is a deliberate, documented design trade-off, not an
open item.
