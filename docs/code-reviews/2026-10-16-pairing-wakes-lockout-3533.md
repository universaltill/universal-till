# Review: a pairing during a D6 lock-out ends the hourly wait (ut-docs#3533)

- **Branch**: `fix/3533-pairing-wakes-lockout-wait`
- **Author**: Opus 5.5, the pipeline's cloud build lane (`lane:cloud-41`).
- **Reviewer**: Sonnet 5.5, as an independent subagent with a fresh context.
  The routing table names Fable for a medium card; the Fable review was
  refused (usage credits, HTTP 429), so Sonnet reviewed instead
  (`MODEL-ROUTING.md` fallback). It is still a different model from the author.
- **Date**: 2026-10-16

## What the card asked, and what was already there

ut-docs#3533 (filed 2026-10-03 during the #3524 review): once a till is
locked out by ADR-0116 D6 (three 401s → flat hourly wait + chip), a new
credential from "Pair with a shop" (#3523) should be tried promptly, not
after up to an hour.

That wake already exists, and it landed after the card was filed. ADR-0148
§2 (#3615) gave every operator action, pairing included, a 2-minute
check-in window. `POST /api/enrol/pair` calls `requestOperatorCheckin`,
which opens the window and kicks `d.CloudSyncNow`. `Start`'s wait loop
ends a backoff *or a D6 lock-out wait* on a kick while a window is open.
The pages layer was already pinned by `TestEnrolPair_SuccessOpensWindow_FailureDoesNot`.
Two things were missing:

1. No loop-level test proved that a pairing during a lock-out ends the
   hourly wait and clears the chip.
2. `Start`'s comment still said that a pairing credential "is first tried on
   the next hourly tick". That has been wrong since ADR-0148.

## What shipped

- `internal/cloudsync/auth_lockout_test.go`:
  `TestPairingDuringLockOutEndsTheHourlyWaitAndClearsTheChip`. It uses the
  real scheduler and the production `tillAuth` tracker. Three 401s cause the
  lock-out and show the chip. A kick with no window open (a cloud nudge or a
  relayed check-in) makes no request. A window plus a kick with a now-working
  cloud runs a check-in within seconds, and the chip clears.
- `internal/cloudsync/cloudsync.go`: the `Start` comment is corrected. There
  is no behaviour change.

## Deliberately not built

The card also said that a retry on an *unchanged* credential must not bypass
the lock-out. ADR-0148 §2 (accepted) says the opposite: "A window wakes the
loop even during an error backoff or an ADR-0116 D6 lock-out wait", for
every operator action. Gating the wake on a credential change would
contradict a binding ADR (ADR-0007). The reviewer agreed. The cost of
leaving it is bounded: each authenticated operator action opens a 2-minute
window and causes at most a few extra 401s, and the streak stays. If the
owner wants the stricter rule, it needs an ADR-0148 amendment first. That
is noted on the card, not built.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The final chip check ran once, straight after `AfterTick`. `sched.next` folds the success into `tillAuth` *after* `AfterTick`, so the read raced it. The reviewer saw 2 of 5 `-count=3 -race` runs fail. | **Fixed**: a deadline-bounded poll for `!Show`, the same pattern the test already uses for the lock-out. Then `-count=20 -race` passed. |
| 2 | minor | The negative assertions (20 ms "no 4th tick", 100 ms "no request after a no-window kick") can falsely pass on a slow machine but can never falsely fail. | Accepted. |
| 3 | nit | The test sets package globals (`firstDelayNS`, `tickIntervalNS`, `newSchedulerFn`, `tillAuth`, the operator window). | Accepted: every one is restored in `t.Cleanup`, the test is not parallel, and `startJoined` joins the goroutine. No race was reported. |
| 4 | info | The other credential paths (claim code, Register now, check-plan, managed TSE, `checkinAfterRegistration`) all open the window too. | No follow-up needed. Replica redeem and rotation run inside the loop or its own retry and were not traced further. |

The reviewer checked the repo rules: there are no file writes, no
cwd-relative paths, no raw SQL and no user-facing strings.

## Verified beyond the test passing

- **Mutation 1.** The kick branch was changed to `if err != nil { continue }`
  (no kick ends a wait, which was the pre-ADR-0148 behaviour). The test fails
  with "the pairing's kick did not end the hourly lock-out wait". This was
  re-run after the fix.
- **Mutation 2.** The kick branch was changed to `if false` (every kick ends
  the wait). The test fails with "a kick with no operator window ended the
  lock-out wait".
- **Gate.** `go vet` passed and `golangci-lint` reported 0 issues. The CI
  guards passed locally, except two that are environmental and do not touch
  this diff: `guard-deadcode-baseline.sh` fails because the local deadcode
  tool was built with go1.26 against a go1.27 module, and
  `guard-shellcheck-version.sh` fails because there is no shellcheck binary.
  CI runs both.
- There is no UI surface or user manual change: the behaviour is unchanged
  and this is a test plus a comment.

## Verdict

Safe to merge.
