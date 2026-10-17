# Code review — LAN TLS full closure: plain-HTTP peer sync closes once every till pinned, TLS-only satellites, TLS-first code join (ut-docs#4411)

Date: 2026-10-15 · Card: ut-docs#4411 (split from #2737; ADR-0114 §7
"Enforcement", last two sentences) · Branch: `feat/4411-lan-tls-close-plain-http`

## What shipped

- **Plain HTTP closes once the fleet has pinned.**
  `refusePlainSyncWhenClosed` (`internal/pages/sync_api.go`) answers 403
  to a plain-HTTP request on the unauthenticated peer handshakes
  (`POST /api/sync/enroll`, `POST /api/sync/pair-request`,
  `GET /api/sync/pair-requests/{id}`, `POST /api/sync/primary-proof`).
  It does this when the till serves TLS, has at least one enrolled till,
  and every enrolled till is in `till_tls_pinned`
  (`TillsRepo.AllTillsTLSPinned`, a new repo method).
  - The check runs before any rate limiter or one-time token.
  - The first refusal per process is logged.
  - A DB error leaves plain HTTP open (logged once). `syncTill` still
    protects every pinned till, and ADR-0114 §7 says TLS must never be the
    cause of a failed request.
  - Bearer routes need nothing new: in a closed fleet every till is pinned,
    so `syncTill` already refuses them.
  - `ping` and every session/browser route stay open.
- **Satellites are TLS-only from enrolment.** A plain-HTTP satellite
  enrolment is refused before the token is spent. This covers both the
  till's own role and the manager's choice on the approval card
  (`RoleForToken`). A satellite enrolled over TLS is marked pinned at once.
  Existing unpinned satellites and later role changes are untouched, so an
  update locks nobody out.
- **A code join tries TLS first.** When `completeJoin` has no pairing pin,
  it sends `enroll` over TLS on the same port and learns the pin
  trust-on-first-use, the same way `sendPairRequest` does.
  - On success, the snapshot download is pinned and the pin is staged as
    `PrimaryCertPin`.
  - It falls back to plain HTTP only when `Exchange` reports `plainOK`.
- **Docs.** ut-docs `architecture/lan-sync.md` now has a "Full closure"
  bullet with a mixed-version note. The "Not yet" list drops #4411, and
  the enrolment sentence in Increment D1 is updated.

## Review

The review was independent and run on Sonnet. MODEL-ROUTING calls for
Fable on a medium Opus-built card, but the Fable review was refused with
"out of usage credits", so it fell back to Sonnet as MODEL-ROUTING allows.
There were no blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A main till without TLS refuses satellites, and the joiner sees only the generic "refused". | **Accepted by design.** ADR-0114 §7 says satellites are TLS-only from day one, and lan-sync.md says so. A dedicated localized join error (plus language packs) is follow-up card ut-docs#4451. |
| 2 | should-fix | The satellite guard ignored a `RoleForToken` read error. A transient DB error could let a manager-chosen satellite enrol over plain HTTP if the later read succeeded. | **Fixed.** A read error is now a 500 before the token is spent (fails closed). |
| 3 | nit | lan-sync.md said "three rules" above four bullets. | **Fixed** (wording). |
| 4 | nit | An older joiner's plain pair-request or poll that gets the new 403 shows a raw status, not "update this till". | **Accepted.** It is documented under "Mixed versions", and the same follow-up card covers it. |
| 5 | nit | If a TLS enroll times out after the request was written, the token may already be spent, so the user has to mint a new code. | **Accepted.** `sendPairRequest` makes the same trade-off, and it avoids double submission. |

Things the reviewer checked and found fine:
- Every replica-to-main client goes through `lantls.NewPeerClient` once a
  pin is held, and only an unpinned till (whose row then keeps the fleet
  open) can still go plain. So a pinned row implies a pin-holding replica,
  and there is no upgrade lock-out.
- `till_tls_pinned` is never synced, so replicas and promoted tills start
  open.
- On the TLS path, the body is closed, redirects are refused
  (`CheckRedirect`), the timeout is unchanged, and the snapshot client is
  pinned to the learned pin.
- A re-join creates a new till row and leaves an existing pin or next pin
  alone.

## TDD re-verified

The reviewer worked in a separate worktree. It reverted the production
changes in `sync_api.go`, `pairing_api.go` and `sync_primary_watch.go`
and ran the tests:

- `TestPlainSyncClosure_ClosedRefusesPlainHandshakesWithoutBurningTheToken`
  failed with `plain enroll once closed = 200 …, want 403`.
- `TestEnroll_SatelliteIsTLSOnlyFromEnrolment` failed with
  `plain satellite enroll = 200 …, want 403`.
- `TestSyncJoin_CodeJoinTriesTLSFirstAndStagesThePin` failed with
  `main till got 2 plain-HTTP /api/sync/* request(s) during a TLS code join`.

All three pass with the fix restored. The open-path regression guards
(`StaysOpenUntilEveryTillPinned` for zero tills, one unpinned till, and
TLS not serving; `PingAndBrowserRoutesStayOpen`;
`CodeJoinFallsBackToPlainWithoutAPin`) pass both before and after,
because they protect what must not change.

## Verified beyond unit tests

- The code-join test drives real same-port TLS and plain sockets (a main
  till serving both). It counts plain `/api/sync/*` requests (0) and checks
  that the staged pin equals the served certificate's pin.
- The e2e specs that enrol over plain HTTP were checked:
  `nav-rail-lock-reachable-1346` and `targeted-swap-tills-backup-2904`
  both enrol an *additional* till (no role) into an unpinned fleet, so the
  change doesn't affect them.
- There is no UI surface, so there was nothing to look at.

## Gate

- `gofmt -l .` produced no output. `go build ./...`, `go vet` and
  `golangci-lint run ./...` all passed.
- `go test ./...` passed.
- The `ci.yml` build guards passed (data-access, i18n, card-data, naming,
  compliance, core-neutral, kiosk-engine, mail, no-showmodal, netaccess,
  page-http-error, help-topics/drift, deadcode-growth, pipefail,
  tests-sandboxed).
- `guard-deadcode-baseline.sh` could not run locally: the container's
  `deadcode` binary was built with go1.26, and the module needs go1.27.
  This is environmental, and CI runs it.

## Verdict

Safe to merge.
