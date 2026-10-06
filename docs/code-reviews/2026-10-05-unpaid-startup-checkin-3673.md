# Review: unpaid till checks in once at start-up; no retry after a 402 (ut-docs#3673)

- **Card:** ut-docs#3673. Branch `fix/3673-unpaid-startup-checkin`.
- **Design:** ADR-0148 "Amendment (2026-10-05)", merged first in ut-docs#3717.
- **Build:** Opus 5.5, `lane:cloud-54`, `complexity:hard`.
- **Review:** Fable 5.1, independent of the author.
- **Split out:** the cloud recording version, platform and last-seen on a 402 → ut-docs#3716.

## What shipped

- **Start-up trigger.** `Start` arms a start-up trigger, which lives
  in `internal/cloudsync/sync_gate.go`. It works like this:
  - A registered till whose gate is closed makes **one** attempt per start.
  - It fires when its version differs from the recorded
    `cloudsync.unpaid_checkin.version` (covers the first run) or today's
    local date differs from `cloudsync.unpaid_checkin.date`.
  - It is re-tested on each gated tick for the first hour after start (on
    the monotonic clock), so a Pi without an RTC still checks in once NTP
    fixes the date.
  - A till with no identity disarms it.
- **Markers.** They are written only when an unpaid POST is answered 2xx
  or 402, never on the paid path. They are per till:
  - `PerTillSettingPrefixes`;
  - `db.TillCloudIdentityPrefixes`, which strips them from the join
    snapshot.
- **Unpaid check-ins POST directly.** They use `planUnpaidCheckin`, with no
  conditional GET, so the request carries the device report and its
  version. Paid tills are unchanged.
- **A 402 closes the operator window** it answered (CAS on the deadline
  read at tick start) and ends the start-up trigger. A paid-cached till
  that gets a block-less 402 still honours Retry-After, and the log line
  says which case applies.
- **v0.30.19 release note corrected** in en/de/tr/fa/ar. It had said
  "waits about an hour between attempts"; it now describes what 0.30.19
  really does: no sync, check-in only on an operator action. The start-up
  check-ins belong to the next release's note.
- **`web/help/*/claim.md`** describes the install, update and
  first-start-of-day check-ins.

## Findings (Fable): 0 blocker, 1 major, 2 minor, 5 nit

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | major | The edit rewrote the already-released v0.30.19 note to claim behaviour that ships later | Fixed: v0.30.19 note now describes 0.30.19 accurately (no hourly sync; operator-action check-ins only); the start-up check-ins go into the next release's note at release time |
| m1 | minor | `closeOperatorWindow` (an unconditional Store(0)) raced an operator opening a window mid-POST → the action's kick was swallowed silently | Fixed: CAS on the deadline seen at tick start; test `TestPlanRequiredKeepsWindowOpenedDuringPost` (failed before: "the window opened during the POST was closed by the 402 it did not cause") |
| m2 | minor | Paid-cached till lapsing via a GET 402 with no Retry-After → a 2nd call the same start from the start-up trigger | Fixed: any 402 disarms the start-up trigger; test `TestPaidTillLapse402EndsStartupTrigger` (failed before: "second tick err = …402, want quiet") |
| n1 | nit | `enroll/pair.go` comment claimed Pair clears all `TillCloudIdentityPrefixes` keys | Fixed: comment corrected (Pair's own wipe list; markers re-recorded by Pair's check-in) |
| n2 | nit | Markers hidden from the "All settings" card (via `credentialSettingKey`) | Accepted: internal bookkeeping, not owner-editable |
| n3 | nit | 402 log line wrong for the block-less paid path | Fixed |
| n4 | nit | `cloudsync.unpaid_checkin.` entry is redundant with `cloudsync.` | Accepted: kept for intent |
| n5 | nit | en note repeated its title | Fixed by M1 rewrite |

**TDD re-verified by the reviewer** in an isolated worktree. With the
production code reverted:
- 14 of 17 start-up tests fail behaviourally. The 3 that pass are
  quiet-path guards.
- `TestRedactedJoinSnapshot_StripsUnpaidCheckinMarkers` fails with
  "rows=2".

All pass once the code is restored. The two review-fix tests were watched
failing before their fixes, as quoted in the table.

## Verified beyond automated tests

A driven run of the real binary was made against a stub cloud. The stub
answers 402 `plan_required` with `Retry-After: 3600` and logs every
request. The till is unpaid, with an env-configured identity.

| Start | Build | Sync requests |
|---|---|---|
| 1, first run | v0.30.20 | exactly 1: `POST /v1/stores/sync` carrying `version: v0.30.20`, no `GET /checkin`; nothing more in 200 s |
| 2, same day | v0.30.20 | 0 |
| 3, upgrade | v0.30.21 | 1 POST (`v0.30.21`) |
| 4, recorded date set to yesterday | v0.30.21 | 1 POST |
| 5, same day again | v0.30.21 | 0 |

The log line reads "checking again at the next start-up or operator
action".

Observed and outside this card: the enrol path's
`GET /ui/api/signing-key` and `POST /v1/stores/devices/register` retries
every 30 s to 2 min. They happened only because the stub answered 404. That
is a §6 / ut-docs#3625 matter.

No UI surface changed except help prose, so there were no screenshots.

## Gate

Run once after the last edit:
- gofmt, `go build ./...`, `go vet ./internal/...`, `go test ./...`;
- every guard in `ci.yml`'s build job;
- `go test -race` for cloudsync, data, releasenotes and db (the db race
  test filtered; its full race run exceeds the sandbox's 10-minute
  timeout, as on a clean tree).

golangci-lint was not run, because the local binary was built with
go1.25 and the module needs 1.27; CI runs it.

## Verdict

Safe to merge.
