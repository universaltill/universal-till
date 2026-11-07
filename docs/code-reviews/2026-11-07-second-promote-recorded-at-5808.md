# Review: a second promote keeps the first promoted till's own late sales in one Z (ut-docs#5808)

**Date:** 2026-11-07 · **Branch:** `test/5808-second-promote-recorded-at` ·
**Docs:** ut-docs `docs/5808-second-promote-recorded-at` (design-5074 §9.3)
**Author:** Sonnet Dev subagent · **Tester:** Opus subagent · **Reviewer:** Opus, independent subagent (different model)

## What shipped
The card (L4 of the ut-docs#5777 review) suspected a gap. B is promoted and
stamps `sales.recorded_at` on its own late sales. Then B dies before its
first Z and a replica C is promoted. C's mirror copy of those sales carries
no `recorded_at`, so the card expected the sales to reach no Z.

BA/Architect showed **this does not happen on main**, and the reviewer
re-checked and agreed:
- **C can only follow B by re-joining it.** Rediscovery
  (`internal/discovery/primary_watch.go`) needs B to prove it holds C's
  pairing hash. The join snapshot and admin pulls redact that hash, and only
  `ApplyReplicaIdentity` writes `sync.primary_url`.
- **The re-join copies B's whole database**
  (`db.RedactedJoinSnapshot`). Neither the snapshot redaction nor
  `ApplyReplicaIdentity` touches `sales.recorded_at` or
  `reports.eod_window_floor`, so both reach C.
- **The copy never wins.** `AdoptCopy` inserts only ids missing from
  `sales`, so C's copy record never replaces the snapshot row.
- **The floor can't move.** B's closes come from its `report_archive`,
  which C already holds.

So no production change was made. The behaviour is pinned by tests instead:
- **New** `internal/pages/promote_second_own_late_5808_test.go`: the full
  second promote, using the real `RedactedJoinSnapshot`,
  `StageReplicaIdentity`/`ApplyReplicaIdentity`, B's real
  `LoadMirrorSales`/`LoadMirrorCloses` state page, the real promote and
  `generateEOD`. C's first Z is 2 sales / 320 (S and S2 once). A sale under
  the floor is left out, a later window counts 0, and S's origin fields are
  unchanged.
- **New** `TestRedactedJoinSnapshot_KeepsRecordedAtAndWindowFloor_5808` in
  `internal/db/join_snapshot_test.go`.
- **Refactor:** helper `newSyncSalesTestDepsOn(t, db)` factored out of
  `newSyncSalesTestDeps` (pure move).
- **Docs:** design-5074 §9.3 gains a paragraph. It says what carries the
  late sales over a second promote, notes the N10 exception, and notes that
  a re-home without re-join (N11) would need the mirror record to carry
  `recorded_at`.

## TDD / red-green (Dev, then re-verified by Tester and Reviewer)
- Snapshot NULLs `recorded_at` → db test `join snapshot sales.recorded_at = { false}`;
  pages test fails. With the precheck off, the Z itself is `1 sales / 70 gross`.
- Snapshot deletes the floor → both tests fail. After the review fix, with
  the precheck off the Z itself is `3 sales / 1320 gross` (re-run by the
  orchestrator).
- Restored → green (`-count=3` by the Reviewer).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | medium | `ApplyReplicaIdentity` calls `secrets.SetDefault(nil)`, which wiped the key store `TestMain` registered for later tests in the package (order-dependent `ErrNoStore`) | Fixed: restore `secrets.Default()` in `t.Cleanup` |
| L1 | low | `report_archive … LIKE '%'||s||'%'` with `n > 1` asserted nothing (the Z JSON holds no sale ids) | Removed; the counts and the 0/0 later window carry the claim |
| L2 | low | The pages test didn't prove the floor end to end: with no sale between C's -24h close and M's close, a lost floor still gave 2/320 (Tester + Reviewer) | Fixed: a sale dated -180 s under the floor; a lost floor now gives 3/1320 |
| N1 | nit | The doc paragraph should mention the N10 replica-Z exception | Fixed |

Accepted residuals (outside this card, as the BA noted):
- **O1:** C's late note names S under C's own id, not B's (the join
  snapshot stores the main till's own sales with an empty till id). Filed
  as a Backlog card (see the close-out comment on ut-docs#5808).
- **O2 (N10):** a replica Z on C before B dies.
- **O3:** an interrupted B adoption that resumes after C's re-sync (not
  reproduced; noted on ut-docs#5808).

## Gates
- `gofmt -l internal/` prints nothing; `go vet` on pages and db is clean;
  `go build ./...` passes.
- Full `go test ./internal/db/ ./internal/pages/` passes without -race
  (Tester).
- Targeted `-race` passes: `-run 'SyncSales|Promote|OwnLate|5808'` on pages
  and `JoinSnapshot|5808` on db.
- Full-package `-race` and `golangci-lint` on db/pages: 0 issues. The
  sandbox is too slow for a full `-race` run, so CI runs it.
- Every `ci.yml` guard passes locally except three tool-version checks:
  `guard-deadcode-baseline{,_test}.sh` (local deadcode built with go1.26)
  and `guard-shellcheck-version.sh`. None is affected by test-only files;
  CI runs them.

Pre-flight: till user-visible behaviour n/a (tests only); `en.json` n/a;
contract n/a (LAN sales-mirror stays 1.2.0, no migration); reference doc
updated (design-5074 §9.3); ADR n/a (no new decision); review record this
file; follow-ups O1 → Backlog card; O2 is design-5074 N10; O3 not reproduced, noted on the card.

**Verdict:** approve.
