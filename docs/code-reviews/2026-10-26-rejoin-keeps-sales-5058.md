# Review: a re-joining till keeps its unsent sales (ut-docs#5058, UT-5)

- **Date**: 2026-10-26
- **Branch**: `feat/5058-ut5-rejoin-keeps-sales`
- **Card**: ut-docs#5058 (owner request: when other tills re-join a new main
  till they must not lose their unsent sales); design §5 / N3; ADR-0011 D3.
- **Reviewer**: independent read-only subagent on a different model (Fable).

## The problem (verified in code)

A join (`completeJoin`) replaces the joining till's whole database with the
main till's snapshot. A joined till queues its own sales (`till_id = ''`,
`created_at > sync.push_cursor`), no-sale opens and cleared baskets for the
main till. When the main till died or was replaced and the till joined the
new one, that queue survived only in the pre-restore copy in the backups
folder: invisible to the shop.

## The choice

Replay, not refusal. Refusing a re-join while sales are queued would block
exactly the shop that needs it (the old main till is gone, the queue can never
drain). The new main till enrols the till first; the till then has a bearer
for it and sends the queue through the same endpoints and the same idempotent
apply (by sale id) the push loop uses. Only after that is the snapshot
downloaded, so the copy the till restarts on already holds those sales.

What shipped:

- `rejoin_replay.go`: for a till that is joined (`sync.primary_url` set), the
  queue past each cursor is sent in batches (sales by the full receipt list,
  so sales sharing a second are never skipped; the others by their
  `(created_at, id)` cursor). Any non-200, or a sale the new main till could
  only quarantine (an item, voucher, user or original sale it does not know),
  fails the join: the enrolment is withdrawn, nothing is staged, the till keeps
  everything (`tills.join_error.unsent_not_sent`, which says to back up and
  retry).
- A last pass after staging (from the stored cursor again; the apply skips
  what the main till has), a pass in the restart button's handler (refuses
  the restart if it cannot send), and a pass at start before the staged copy
  is applied (covers Windows, where the till cannot restart itself; a failure
  there is logged as an error, the sales stay in the pre-restore copy).
- Receipt numbers: the sales keep their own `receipt_no`. The new main till
  now records the prefix it grants (`tills.receipt_prefix`, migration 092,
  gated in the admin generation) and keeps a re-joining till's own prefix
  when no other till holds it or has sales under it; otherwise it numbers past
  every prefix in use. So tills re-joining in another order keep numbering
  where they left off and never collide with each other's queued sales.
- Day closes are not journaled to a main till (each till uploads its own Z
  chain to the cloud). In retention mode cloud/both a re-join is refused,
  before enrolling, while some are not acknowledged by the cloud
  (`unsent_day_closes`, or `unsent_day_closes_refused` when the cloud refuses
  uploads because the subscription is inactive).
- A standalone till's first join is unchanged (its sales are its own
  history; the explicit "replace this till's sales" confirm stays).
- Tills page on a joined till: the unsent count and what a re-join does, a
  re-join variant of the "has sales" warning, and in the "main till isn't
  answering" notice what to do if it was replaced. Help step 4 of "When a
  joined till can't reach the main till" in en/de/ar/fa/tr.

## Findings (independent review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Major | A sale the new main till quarantines answered 200 and counted as sent, then was wiped. | Fixed: quarantined > 0 on the sales journal fails the join. Test. |
| 2 | Major | Prefixes by join order: re-joining in another order collides on `receipt_no` (UNIQUE) with no way out. | Fixed: migration 092, `chooseTillNo` keeps the till's own prefix when free, else numbers past all in use. Tests (order swapped; prefix taken). |
| 3 | Major | Sales rung between the last pass and the restart were lost (manual restart, Windows). | Fixed: pass in the restart handler (refuses the restart on failure) and at start before the apply. Test (mutation-checked). |
| 4 | Major | `rejoinReplay.any()` unused (lint). | Removed. |
| 5 | Minor | Same-second boundary in the last pass. | Fixed: last pass from the stored cursor. |
| 6 | Minor | A partial replay before a failed join leaves those sales on the new main till under a withdrawn till id. | Accepted: the sales are kept (not lost), the retry skips them; only the per-till attribution of those few sales is off. Follow-up card. |
| 7 | Minor | Day-close gate can wedge when the cloud refuses uploads; wrong advice. | Fixed: separate message naming the subscription and the retention setting. Test. |
| 8 | Minor | Contradicting copy on the Tills page. | Fixed: joined-till variants; the old "only once no sales are waiting" key removed (no leftovers). |
| 9 | Minor | Voids are not journaled (pre-existing ADR-0011 gap). | Out of scope; help no longer says "not lost" without qualification. Follow-up card. |
| 10 | Minor | ar "main till" has two renderings in the file. | Accepted: both exist in neighbouring strings (the setup wizard uses الرئيسي). |
| 11 | Nit | The `rejoin_journal_sent` audit lands in the DB the restart replaces. | Accepted (same as `joined_primary`); the log line survives. |

Reviewer verified: pinned TLS reuse (the replay uses the exact client and base
the enrolment settled on), no re-push after restart (replayed sales come back
with the new till id; `ApplyReplicaIdentity` resets the cursors), the staged
snapshot is taken after the replay in the same request, standalone joins
unchanged, no SQL outside `internal/data`.

## Verified beyond automated tests

Tests use real migrated temp DBs on both sides and the real join route and
main-till endpoints (no stubs). TDD: the replay tests fail with the replay
disabled (mutation: 4 failures); the start-time pass test fails with it
disabled; the prefix tests failed before `chooseTillNo` (T2- granted).

Driven run: throwaway till (temp `UT_DATA_DIR`, auth off, port 8198) set up as
a joined till with an unsent sale; Tills page in en 1024×600 and ar 360px:
the note shows the count, wraps, no horizontal scroll. Not looked at: the
"main till isn't answering" notice (the throwaway till did not reach that
state), real hardware, dark theme on the Tills page, de (pack not installed);
no run against two real tills on a LAN.

## Gate

`go build`, `go vet ./internal/...`, `gofmt`, `go test ./...` (all green),
every guard in `ci.yml`'s build job (local failures only
`guard-shellcheck-version` and `guard-store-private`, environment, failing on
`origin/main` too). `make docs-shots` cannot run on macOS (font pin,
ut-docs#4407): must run on Linux before merge.

## Verdict

Safe to merge after a Linux `make docs-shots` on the rebased branch. No cloud
dependency.

## Deferred (cards)

- Re-stamp provenance of sales left by a withdrawn enrolment (finding 6).
- Journal voids from joined tills (ADR-0011 gap, finding 9).
- `db.ApplyPendingRestore` deletes the old DB's `-wal` before setting it
  aside: after an unclean exit the pre-restore copy can miss the newest
  transactions.
- `syncPushSales` advances to the batch's max `created_at`: a sale in the
  same second beyond the batch is skipped forever (pre-existing).

- Manual screenshots: regenerated `web/help/img/*/multitill.png` and `manifest.json` on Linux (ubuntu:24.04 amd64 container, Go 1.27.1, Node 26, fonts-inter 4.0 + DejaVu + Noto Color Emoji, `make docs-shots`, ut-docs#4407); `guard-docs-shots.sh` passes. The other 120 PNGs regenerated with sub-pixel text differences and were left as committed to avoid unrelated churn.
