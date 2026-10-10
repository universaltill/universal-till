# Review: deterministic pairing-expiry test (ut-docs#4040)

Date: 2026-10-09 · Branch: `fix/4040-pairing-expiry-test-clock` · Lane: `lane:cloud-24`
Author: Opus 5.5 · Reviewer: Fable (independent subagent, different model)

## What shipped

- `internal/data/pairing_repo.go`: `PairingRepo` gets an unexported
  injectable clock (`now func() time.Time`). `NewPairingRepo` defaults it to
  `time.Now`, and all six runtime reads (Create, ListPending,
  ListPendingReadOnly, GetByID, ApproveWithRole, RoleForToken) go through it.
  Production behaviour is unchanged.
- `internal/data/pairing_repo_test.go`: `TestPairingRepo_ApproveExtendsExpiry`
  now drives a fake clock instead of a 50 ms TTL plus `time.Sleep`. The
  sequence is: create with a 2 s TTL, approve at +1 s with a 10 min
  extension, then read at +6 s; the row must still be there. A control row
  that is never approved must have expired at +6 s, and `Approve` on it must
  return `ErrNotPending`.

## Root cause

The CI failure came from a 50 ms TTL racing a busy runner. There is a
second cause: `expires_at` is stored as RFC3339 at **second** precision.
A sub-second TTL therefore truncates down to the creation second, and
`Approve` fails whenever a second boundary falls between create and approve,
even without load.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | A future `&PairingRepo{db: db}` literal would leave `now` nil and panic. Every construction site today goes through `NewPairingRepo` (verified with grep). | Accepted. This matches `internal/secrets/keystore.go`. |
| 2 | Info | The test sets the unexported field directly. | Accepted. It is a same-package test. |

No other wall-clock pairing races were found. Every other
`CreatePendingRequest*` caller uses a TTL of 1 min or more, or a negative TTL.

## Verified

- TDD: the new test fails to compile before the field exists. A mutant
  `Approve` that does not extend `expires_at` fails at
  `pairing_repo_test.go:127` ("expected the approved row still retrievable
  past its original expiry"). With the fix restored, the test passes.
- Card acceptance: `go test ./internal/data -run TestPairingRepo_ -count=50 -race`
  passes while a full `./internal/data` run loads the machine in parallel.
  The reviewer re-ran it and it passed.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint ./internal/data/...`
  (0 issues), `go test ./internal/data/... ./internal/pages/...`, and the
  data-access, card-data, kiosk-engine, netaccess and core-neutral guards.

## Verdict

Safe to merge. Test and test-seam only; no user-visible change, so no
help or docs update.
