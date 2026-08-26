# Code review: LAN-sync journal poison-entry quarantine (ut-docs#1127)

**Date:** 2026-08-26
**Card:** universaltill/ut-docs#1127
**ADR:** [0065](https://github.com/universaltill/ut-docs/blob/main/adr/0065-lan-sync-journal-poison-entry-quarantine.md)
**Dev:** Sonnet (subagent) · **Review:** Opus (independent subagent, isolated worktree)

## What shipped

`applyJournal` (`internal/pages/sync_sales.go`) previously rejected the
**whole pushed LAN-sync batch** on any error, and the replica's own push
loop (`syncPushTick`) never advanced `sync.push_cursor` past a rejected
batch — one permanently-failing entry wedged that replica's entire
subsequent replication forever, with no operator-visible signal beyond a
generic rejected-push log line and no escape hatch.

- `internal/db/migrations/074_sync_journal_quarantine.sql` (new,
  append-only) — `sync_journal_quarantine` table, durable record of a
  quarantined entry (till id, sale id, receipt no, reason, full journal
  payload, timestamp), `UNIQUE(sale_id)`.
- `internal/data/voucher_repo.go` — new `ErrVoucherIDExists` sentinel on
  `CreateVoucher` (unique-violation wrap, same pattern as
  `ErrPromotionCodeExists`).
- `internal/data/sync_quarantine_repo.go` (new) —
  `InsertJournalQuarantine`/`ListJournalQuarantine` repository methods.
- `internal/pages/sync_sales.go` — `permanentJournalFailureReason(err)`, a
  small explicit allowlist (`data.ErrVoucherNotFound`,
  `data.ErrVoucherIDExists`) classifying a journal-apply error as
  permanent/non-retryable; `applyJournal` gains a `quarantineReason`
  return; a matched failure quarantines (skip + record, `err` stays nil)
  instead of rejecting the whole batch; `registerSyncSales`'s response and
  audit-log entry gain a `quarantined` count alongside `applied`/`skipped`.
- `ut-docs/adr/0065-...md` (new) — amends ADR-0011 §5, extends ADR-0036
  point 3's "surface as a Problem, never silent" precedent to the case
  where a sale cannot be applied at all.
- `ut-docs/reference/contracts/pos-lan-sync-journal.md` bumped
  1.3.0 → 1.4.0 (additive `quarantined` field; documents the new
  quarantine-vs-422 split).
- Tests: `internal/data/voucher_repo_test.go`,
  `internal/data/sync_quarantine_repo_test.go`,
  `internal/pages/sync_sales_quarantine_test.go` (both trigger scenarios
  at the `applyJournal` level, the allowlist classification, an
  HTTP-level batch test, and — added during review, see below — a
  `syncPushTick`-level cursor-advance test).

## Independent review (Opus, isolated worktree)

Full findings and both TDD-reverify results are in the review agent's
report (summarized here; not re-transcribed in full).

**Verdict: safe to merge. No blockers.**

Verified independently, not taken on trust:
- `errors.Is` chain is real (reverted the `ErrVoucherIDExists` mapping,
  confirmed the raw SQLite `UNIQUE constraint failed` error reaches
  `applyJournal` unwrapped through `CompleteSale`, restored).
- **The load-bearing claim no shipped test covered**: does the replica's
  `sync.push_cursor` actually advance past a quarantined entry, or could
  the client-side push loop still treat `quarantined` as "not applied" and
  retry forever, defeating the whole fix? Traced `syncPushTick`
  (`sync_sales.go`): it advances on `resp.StatusCode == 200` alone and
  never parses the response body, so a quarantined entry (which still
  returns `200`) genuinely unwedges the replica. Proved this empirically
  with a throwaway `syncPushTick`-level test (poison entry → cursor
  advances; same test with `permanentJournalFailureReason` neutered →
  replica visibly wedged, `push_cursor` stays empty, `422`). Test deleted
  after proving the point — **promoted into a permanent regression test
  during triage below**, since the review itself flagged that no shipped
  test pins this.
- Allowlist can't over-match (only one `UNIQUE` on `vouchers` — the `id`
  PK); `ErrVoucherNotFound` can't be a transient race (vouchers aren't in
  the replica-pull table list, so issue-then-redemption on the same
  replica is always ordered issue-first); `missingJournalFields` runs
  before quarantine can fire, so `sale_id` is guaranteed non-empty when
  `InsertJournalQuarantine` runs; the `UNIQUE(sale_id)`
  idempotency path is actually exercised by a test; migration 074 is a
  new file, no existing migration edited; no template/help-topic/locale
  file touched (correct — this is a pure backend/ops-visibility change,
  no operator UI added); no real client/shop name in any test fixture
  (Alice/Bob/"Sample Holder"/"Replica 1").

### Findings and disposition

| # | Severity | Finding | Disposition |
|---|---|---|---|
| 1 | should-fix | A quarantined sale is silently-dropped revenue: the only signal is a 50-entry in-memory log ring, and the replica's own sync status reads "fully synced" since the cursor correctly advances past it. | **Filed as follow-up**: universaltill/ut-docs#1133 (admin UI / durable visibility). Explicitly out of scope per ADR-0065's own "Not decided here" section — not bundled into this change. |
| 2 | nit | No test pins the cursor-advance claim itself; a future change making `syncPushTick` retry on `quarantined > 0` would reinstate the wedge with every existing test still green. | **Fixed**: added `TestSyncPushTick_QuarantinedEntryAdvancesCursor` — a real replica→primary push of a poison entry (voucher redemption against an unknown voucher, built via a real DB-seeded sale so `buildJournal`/`GetSaleDetail` produce it, not a hand-built journal bypassing that path), asserting `sync.push_cursor` actually advances and the primary durably records the quarantine. |
| 3 | nit (pre-existing) | `LocalSalesSince` (`internal/data/pos_repo.go`) orders queued sales by `created_at` alone with no tiebreak — a same-timestamp issue+redemption could journal out of order. Low probability, pre-existing, not introduced by this change. | **Documented**: added a Consequences bullet to ADR-0065 naming the risk and the fix shape (an explicit tiebreak) as a follow-up, not bundled here. |
| 4 | nit | `payload, _ := json.Marshal(j)` in `quarantineJournalEntry` discarded the error; an unreachable-in-practice failure would silently leave `payload_json` empty, defeating the future manual-replay path the column exists for. | **Fixed**: error is now checked and logged (`Errorf`) before persisting, so a marshal failure is loud rather than silently swallowed. |
| 5 | housekeeping | Branch carried 3 `WIP:` checkpoint commits (this pipeline's own mid-review safety net, ut-docs#386 — background dev subagent was still writing when the environment's stop-hook forced a commit). | Squashed into one commit before merge, `--reset-author` to keep correct attribution. |

## Verification beyond automated tests

- Full gate re-run by the orchestrator (not just trusted from Dev/Tester
  or the review subagent): `gofmt -l .` clean, `go build ./...` /
  `go vet ./...` clean, full `go test -count=1
  $(go list ./... | grep -v /internal/plugins)` — every package `ok`, no
  `FAIL`.
- CI-blocking guards run locally and green: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-i18n.sh`, `guard-plugin-menu-read.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`. `guard-adr-index.sh` (ut-docs) green for the
  new ADR.
- **TDD claims re-verified twice, independently**: once by the
  orchestrator (neutered `permanentJournalFailureReason` to always return
  `""`, confirmed all 3 original quarantine tests fail with the exact
  pre-fix symptom — `422`, whole batch rejected — restored, confirmed
  green); once by the Opus reviewer in an isolated worktree (reverted the
  `ErrVoucherIDExists` classification specifically, confirmed the raw
  constraint-violation error surfaces, restored).
- **GitHub Actions has stopped triggering on this repo entirely as of
  this cycle** (filed separately as universaltill/ut-docs#1131 — a
  platform/webhook issue, not a code problem, confirmed on multiple
  unrelated PRs and pushes). CI confirmation for this change is therefore
  the local full-gate re-run above, substituting for the GitHub Actions
  check exactly as this cycle already did for PR #561/ut-docs#1027 — not
  a lowered bar, the same commands CI itself runs.

## Explicitly deferred (not blockers)

- No admin UI to browse `sync_journal_quarantine` and no durable
  operator-facing summary beyond the Warnf/log-ring — universaltill/ut-docs#1133.
- No manual re-apply/replay path for a quarantined entry — `payload_json`
  retains everything a future tool would need; building that tool is
  separate work (ADR-0065 "Not decided here").
- The pre-existing `LocalSalesSince` ordering tiebreak gap — documented in
  ADR-0065's Consequences, not fixed here (out of scope, low probability,
  predates this change).

## Verdict

**Safe to merge.** No blockers found by independent review; the one
should-fix is filed as a tracked follow-up per the deferring ADR's own
scope; both nits worth fixing were fixed and re-verified; the one nit
that's genuinely pre-existing and out of scope is documented in the ADR
rather than silently dropped.
