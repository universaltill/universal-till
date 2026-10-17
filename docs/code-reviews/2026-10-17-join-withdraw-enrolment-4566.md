# Review — a failed join withdraws its own enrolment (ut-docs#4566)

Branch `fix/4566-join-withdraw-enrolment`. Built on Opus 5.5 (`complexity:medium`).
The Fable review was refused because the usage pool ran out, so Sonnet 5.5
reviewed instead, with fresh context in an isolated worktree
(`MODEL-ROUTING.md` fallback).

## What shipped

- **Main till: `POST /api/sync/enroll/withdraw`.** It is bearer-gated via
  `syncTill`, so the TLS-pin rules apply. It deletes only the bearer's own
  till, never an id from the request. It works only within 15 minutes of
  `tills.enrolled_at` (`withinJoinWithdrawWindow`: the SQLite `datetime`
  format or RFC 3339, one minute of skew allowed, fails closed). It is
  refused on a replica. It also does the following:
  - Disconnects the link.
  - Removes the auto-provisioned enrolment register, through the new
    `POSRepo.DeleteUnusedEnrolmentRegister`. This happens only when no
    sale, shift, no-sale event or FK-protected row uses that register.
  - Writes an audit row, `till_enrolment_withdrawn`.
  - Sends `HX-Trigger: tills-changed`.
- **Joining till: `completeJoin`.** Every failure after the enrolment now
  calls `withdrawEnrolment` and still returns the original error. That
  covers the snapshot request, download and non-200, staging, identity
  staging, and a sale landing mid-join. `withdrawEnrolment` is
  best-effort: it uses the same pinned client and base, a 10 s timeout,
  and `context.WithoutCancel`.
  - When identity staging fails, `completeJoin` now also unstages the
    already-staged snapshot. Before this change, a restart would have
    restored the main till's copy with no replica identity.
  - On both the identity and has-sales paths, the enrolment is withdrawn
    only once `UnstageJoin` has succeeded. A join that is still staged
    applies on restart, so it must keep its enrolment.
- **Registration lists.** The new path is on the auth-middleware exempt
  list (and its test) and on the demo-mode denied list.
- **Manual.** `web/help/{en,de,tr,ar,fa}/multitill.md` step 3 now says the
  till takes itself off the main till's list. A manual revoke is needed
  only if the main till was unreachable at that moment.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The auto-provisioned register stayed behind, so a retry produced "Till 2 (2)" and orphan registers that synced to every replica. | **Fixed.** The withdraw handler removes the register the enrolment created when nothing uses it (looked up with `EnrolledRegisterID`). The retry test asserts that exactly one register "Till 2" exists. |
| 2 | minor | If the enrol response is garbled after the main till committed the enrolment, there is no bearer to withdraw with. | **Accepted.** It can't be fixed without a bearer. The manual's "unreachable → revoke" sentence covers it. |
| 3 | minor | For 15 minutes after enrolling, a fully joined till could withdraw itself. | **Accepted.** It can only remove itself, never another till; the bearer is 256-bit. |
| 4 | minor | The German sentence was tautological ("während des Beitretens"). | **Fixed:** "Bricht das Beitreten zwischendurch ab". |
| 5 | minor | The Arabic sentence had the same tautology. | **Fixed:** "في منتصفه". |
| 6 | nit | Withdraw sent no `tills-changed` trigger. | **Fixed.** The header is now sent. |

## Verified

- **TDD.** With the three source files reverted (and a stub for the helper),
  the reviewer and I each saw these five tests fail:
  - both same-name-retry tests (mid-join sale, snapshot failure);
  - the own-till-only test;
  - the established-till refusal;
  - the replica refusal.
  All five pass with the fix.
- **Driven run, beyond unit tests.** The retry tests run a real main till's
  `registerSyncAPI` behind an `httptest` server, joined by a real replica
  handler. Enrolment, snapshot, withdraw and re-enrol all go over HTTP.
- **Full gate on the final tree:**
  - `gofmt`, `go vet ./...` and `go test ./...`;
  - `golangci-lint`;
  - every guard in the `ci.yml` `build` job;
  - `shellcheck`.

## Verdict

Safe to merge. Deferred: nothing new. The revoke path (`till_revoked`) also
leaves the register behind, but a revoked till's register usually has
history and must stay, so it is not a defect.
