# Code review: cash-reconciliation tip-held-out arithmetic identity (ut-docs#1124)

**Branch:** `fix/1124-cash-recon-tip-identity`
**Author:** autonomous SDLC pipeline (Dev: inline, Sonnet — `complexity:easy`;
Review: fresh-context Sonnet subagent, `isolation: "worktree"`, independent
of the Dev reasoning, per the `complexity:easy` review tier)

## The card, and what BA verification found

ut-docs#1124 was filed as a follow-up from the ut-docs#1046 review: once
cash tipping is turned on, the printed Z-report's CASH RECONCILIATION
block's visible arithmetic ("Opening float + Cash sales + Pay-ins +
Pay-outs + Skim") wouldn't visibly close against Calculated, with nothing
on the receipt or in the help topic explaining why.

**Verifying current state before scoping (BA step) found the premise was
already half-resolved.** The #1046 commit that filed this follow-up had
*already* added a "Tips held out" print line to `eod_api.go`, positioned
between "Cash sales" and "Pay-ins" — i.e. inside the group of line items
printed immediately above "Calculated". Tracing `ComputeExpectedCash`
(`internal/data/pos_repo.go`) and `CloseShift` (`internal/pos/shifts.go`)
confirmed the real identity is `OpeningFloat + CashSales + TipsHeldOut +
PayIns + PayOuts == Calculated` (Skim excluded, not "+ Skim" as the
original issue's shorthand formula had it) — and that identity already
held with the existing code; it just had **zero regression coverage**
locking it in, and the help topic didn't explain it.

So this card was rescoped from "fix broken arithmetic" to "add the missing
regression coverage, and make the doc/help text actually explain the
identity" — a smaller, more accurate scope than the card's own text
implied, per the BA skill's "if the task is already done or partially
done, say so plainly and rescope" instruction.

## What shipped

- **`internal/data/pos_repo_cash_recon_test.go`** —
  `TestEndOfDay_CashReconciliation_ExcludesCashTips` now computes
  `Calculated` via the real `ComputeExpectedCash` path (previously a
  hand-picked literal that happened to satisfy the identity, not a
  genuine end-to-end computation) and asserts
  `OpeningFloat + CashSales + TipsHeldOut + PayIns + PayOuts == Calculated`
  explicitly.
- **`internal/pages/eod_test.go`** —
  `TestBuildEODDoc_CashReconciliation_TipsHeldOut` gained a self-
  consistency guard on its own fixture (same identity), so the printed-
  line assertions below it are checking a scenario that can actually
  occur against real data.
- **`internal/data/pos_repo.go`** — `CashReconciliation.TipsHeldOut`'s doc
  comment now spells out the identity and its scope (see "Independent
  review" below for why the scope matters).
- **`web/help/{en,fa,tr,ar}/reports.md`** — the "Cash reconciliation on the
  day-end report" section now explains *why* the block's figures add up
  once cash tipping is on, and ties "Skim to safe" explicitly to being
  entered when closing the shift. Edited identically across all four
  locale files (matching this section's pre-existing verbatim-English
  convention across locales).
- **`web/help/img/manifest.json`** — regenerated via `make docs-shots`;
  only the `reports` topic's four locale hashes changed. Two incidental
  PNG-byte diffs surfaced across two `make docs-shots` runs during this
  work (`web/help/img/en/users.png`, then `web/help/img/tr/sell.png`)
  with no markdown/app-surface change behind either — non-deterministic
  screenshot capture (font AA / timing), not a real drift. Both reverted;
  neither topic's manifest hash changed, confirming they weren't real.
- No schema/behavior change: this diff touches tests, comments, and docs
  only. `internal/pos`, `internal/data`'s query logic, and `eod_api.go`'s
  print builder are unchanged.

## Independent review

Spawned a fresh-context Sonnet subagent, `isolation: "worktree"`, briefed
with the diff scope, told to independently re-derive the arithmetic
identity from the actual code (not take the diff's comments on faith),
and to do its own revert-then-restore TDD verification.

**Initial verdict: NOT SAFE TO MERGE — one BLOCKER.** The reviewer
independently disproved the *unqualified* version of the identity claim I
had written: it traced `pos.RecordCashAdjustment` (accepts `Type:"skim"`
on an **open** shift — `ShiftOpenExists`, not "shift must be closing") and
`data.SumShiftAdjustments` (nets **every** `cash_adjustment` row into
`Calculated` with no `type` filter) against
`data.CashReconciliationForLocalDay`'s adjustments query (buckets **every**
`type='skim'` row into `rec.Skim`, excluded from the printed sum,
regardless of *when* it was written). Net effect: a skim recorded
**mid-shift** (via `POST /api/shifts/adjustment`, `type=skim`, on an open
shift) is netted into `Calculated` but still excluded from the printed
identity — breaking it by exactly that amount. The reviewer wrote and ran
a throwaway repro (removed after, tree left clean) confirming this with
concrete numbers (`OpeningFloat=10000 ... Skim=-3000 Calculated=7000`,
visible sum `10000 != 7000`).

**Not reachable via the shipped operator UI** — `web/ui/pages/shifts.html`'s
mid-shift adjustment form only offers `payout`/`adjustment` in its type
dropdown; `skim` only appears on the shift-**close** form. But per this
repo's own ut-docs#1046 precedent ("the 'structurally impossible' premise
doesn't hold at the API layer... a client-side-only gate is not a
guarantee"), reachable-at-the-API-layer-only still counts as real, not
dismissed.

**Verified this finding personally** before accepting it: read
`RecordCashAdjustment`, `SumShiftAdjustments`, `CashReconciliationForLocalDay`,
and `shifts.html`'s adjustment-type dropdown directly — confirmed all of
the reviewer's claims against the actual source, not taken on the
reviewer's word.

**Fix applied**: narrowed every claim this diff makes to what's actually
true —
- The doc comment on `TipsHeldOut` now states the identity holds
  specifically for "a day whose only skim (if any) was recorded at shift
  close" (every path the shipped UI offers), explains the mid-shift-skim
  exception precisely (with the exact functions involved), and links
  ut-docs#1146 (filed as a new Backlog card, see below) rather than
  asserting a universal invariant that doesn't hold at the API layer.
- Both test comments were corrected to stop claiming coverage they don't
  have — neither test records any skim, so neither exercises or is
  invalidated by the mid-shift-skim gap; the comments now say so
  explicitly instead of implying the identity always holds.
- The help topic's "Skim to safe" sentence now ties the claim to "entered
  as part of closing the shift" — the only entry point the shop-owner-
  facing UI actually offers — rather than asserting an internal
  implementation-level guarantee the API doesn't actually enforce. Applied
  identically across en/fa/tr/ar.
- **Filed ut-docs#1146** ("Mid-shift skim adjustment breaks the printed
  CASH RECONCILIATION arithmetic identity") for the underlying gap itself
  — genuinely a different bug (skim timing, not tips) from what #1124 was
  scoped to fix, and picking between the two real fixes (reject
  `type=skim` on an open shift vs. rework the reconciliation query's
  bucketing) is a design decision for its own BA/Architect pass, not a
  patch bolted onto this diff.

**Re-review after the fix**: re-read the corrected doc comment, both test
comments, and all four help-topic edits myself against the reviewer's
exact findings — confirmed every claim now matches what the code actually
does (no remaining reference to Skim being "always" or "deliberately"
excluded without the close-time qualifier). Re-ran the full gate below
after the fix, not just the specific case the finding named.

**Verdict: SAFE TO MERGE**, with the mid-shift-skim gap tracked as
ut-docs#1146 rather than fixed here.

Everything else the reviewer checked came back clean: both changed test
files pass (`go test ./internal/data/... -run TestEndOfDay_CashReconciliation
-v`, `go test ./internal/pages/... -run TestBuildEODDoc -v`); its own
revert-then-restore of `rc.CashSales -= tp.Amount` reproduced the expected
`CashSales`/identity mismatch, then passed clean after restoring, tree
byte-identical; `gofmt`/`build`/`vet` clean; `guard-data-access.sh`,
`guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-compliance-claims.sh` all pass; help-topic text verified byte-
identical across en/fa/tr/ar; diff scope exactly the files described, no
stray PNG bytes; no real client/shop names, no secret-shaped literals,
correct `int64` minor-units usage throughout the test edits (no
`money.Money` misuse).

## Tests

- `internal/data/pos_repo_cash_recon_test.go`:
  `TestEndOfDay_CashReconciliation_ExcludesCashTips` (extended) — computes
  `Calculated` via the real `ComputeExpectedCash` path and asserts the full
  reconciliation identity.
- `internal/pages/eod_test.go`:
  `TestBuildEODDoc_CashReconciliation_TipsHeldOut` (extended) — fixture
  self-consistency guard on the same identity.

**Re-verified personally** (both my own pass and the independent
reviewer's, described above): temporarily commented out
`rc.CashSales -= tp.Amount` in `dateRangeSummary`, re-ran
`TestEndOfDay_CashReconciliation_ExcludesCashTips`:

```
--- FAIL: TestEndOfDay_CashReconciliation_ExcludesCashTips (0.19s)
    pos_repo_cash_recon_test.go:342: CashSales: want 40000 (tip held out), got 42000
    pos_repo_cash_recon_test.go:357: reconciliation identity broken: OpeningFloat(10000)+CashSales(42000)+TipsHeldOut(2000)+PayIns(0)+PayOuts(0) = 54000, want Calculated 52000
```

Restored, re-ran: both tests pass again, `git diff`/`git status` confirmed
the tree came back byte-identical.

## Gate run

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- Targeted: `go test ./internal/data/... ./internal/pages/... -run
  "TestEndOfDay_CashReconciliation|TestBuildEODDoc"` — clean, including
  `-race`.
- Full `go test ./... -race` — every package passed except
  `internal/plugins`, which hit the default 10-minute `-race` timeout —
  the same pre-existing sandbox resource-contention issue already tracked
  as ut-docs#1119 and hit by the #1046 review before it, unrelated to this
  diff (which never touches `internal/plugins`). Re-ran the specific test
  in isolation to confirm: `go test ./internal/plugins/... -run
  TestHostHTTPRetryDifferentRequestNotCached -race -timeout 20m -v` —
  passed in 20.23s.
- All CI-blocking guards relevant to this diff: `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh` (after
  `make docs-shots`, twice — see the manifest note above),
  `guard-compliance-claims.sh` — all pass.

## Non-goals confirmed unchanged

No cash-tipping feature or toggle added — this stays pure verification/
documentation of the reconciliation display, matching #1124's own
non-goals. The mid-shift-skim gap is real product behavior, not touched
here — tracked separately as ut-docs#1146.

## Deferred / out of scope

- **ut-docs#1146** (new): mid-shift skim breaks the printed reconciliation
  identity — a real, currently-reachable-at-the-API-layer bug, filed with
  full repro details and two candidate fixes for its own BA/Architect
  pass.
