# Code review — link-status chip: order contacts on the monotonic clock (ut-docs#2915)

**Date:** 2026-09-27 · **Branch:** `fix/2915-link-status-monotonic-contact` · **Lane:** cloud-54
**Reviewer:** independent subagent (Opus 5.5, fresh context; `complexity:easy`)

## What shipped

- `internal/pages/link_status.go`: `deriveLinkView` orders "was the main till
  reached after X" through `linkInputs.heardAfter`. When this process has
  reached the main till, its own contact time is compared with the link's
  `LostAt`/`LostSeen`/`now`. All of these carry monotonic readings, so a
  wall-clock step between them no longer flips the chip or its "since". A
  step can come from an NTP correction, or from a Pi without an RTC setting
  its clock after boot. Before the first contact since start, the stored
  `sync.last_contact_at` string decides, as it did before.
- `common.Deps.MarkMainContact` / `MainContact`: an in-memory twin of
  `sync.last_contact_at` (an `atomic.Pointer[time.Time]`, following the
  `heldGen` precedent).
- `recordMainContact(ctx, d, at)`: every write of `sync.last_contact_at`
  goes through it and sets both the stored string and the twin. The writers
  are the link hello and refresh, a successful pull, and a successful push.
- `wallStepped` moved from `internal/fleetlink` tests to
  `internal/testsupport.WallStepped` so `pages` tests can use it. Only test
  files import `testsupport`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The pull's contact was stamped when `recordMainContact` ran, after `syncAssets`/`ApplyAdmin` (up to the 60 s client timeout). Before, it was stamped when the admin answer was decoded. A pull in flight while the link's loss was noticed would then count as contact after the loss, and the chip would read "polling" instead of "unreachable". | **Fixed**: `recordMainContact` takes `at`, and the pull passes the time the admin answer arrived. Regression test `TestSyncPull_ContactIsTheAdminAnswerNotTheTickEnd` has the primary stall its follow-up requests; the test failed before the fix and passes after. |
| 2 | nit | `markLinkContact` is a one-line pass-through. | Accepted: it keeps the "a hello is contact" call sites readable. |
| 3 | info | `WallStepped` (which uses `unsafe`) is in a non-test file of `testsupport`. | Accepted: no production importer, no guard scans for it, no import cycle. |

The reviewer found nothing else. It checked that every compared time keeps
its monotonic reading: `.UTC()` is used only for display, and nothing is
rounded or serialised. It found no other writer of the key. It found no
stale-twin case: promote clears `sync.*` and ends replica mode, and joining
another main till takes effect only after a restart, which builds a fresh
`Deps`.

## TDD, re-verified

- All 5 subtests of `TestDeriveLinkView_WallStepBetweenLossAndContact` fail
  without the `heardAfter` monotonic branch: clock stepped back or forward,
  watch "since" choice, and the failed-dial window. The author ran this and
  the reviewer re-ran it in its own worktree.
- `TestSyncPull_RecordsMonotonicMainContact` fails without
  `d.MarkMainContact`, and without `in.Contact = d.MainContact()`
  (checked by the reviewer).
- `TestSyncPull_ContactIsTheAdminAnswerNotTheTickEnd` failed before the
  finding-1 fix, with the contact recorded after the stalled follow-up began.

## Gate

`gofmt -l` clean · `go build ./...` · full `go test ./...` green (before
the finding-1 fix) · `internal/pages`, `internal/fleetlink` and
`internal/testsupport` green after it · `golangci-lint` 0 issues · the
`build` job's guards pass locally. Two exceptions, both environment-only:
`guard-shellcheck-version` (no shellcheck binary in this container; no
shell changed) and the apt-get retry step. The docs-shots surface hash was
refreshed without regenerating screenshots (`Docs-Shots-Unchanged`): the
chip's markup is unchanged, and only its state after a wall-clock step
differs.

## Not verified beyond tests

No driven run. A wall-clock step can't be reproduced on a live till in this
container. The visual chip states are unchanged and already covered by
`TestLinkChip_RendersEachState`.

## Verdict

Safe to merge.
