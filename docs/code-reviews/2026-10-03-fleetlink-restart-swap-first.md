# 2026-10-03 — fleetlink test harness: swap the hub before closing the old one (ut-docs#3570)

## What shipped

`TestClient_StatusFollowsTheLinkLifecycle` failed on a loaded CI runner
(universal-till#1664) with `status_test.go:49: timed out waiting for relinked`.

**Root cause.** The card guessed jittered redial backoff. That was wrong: test
backoff is 10–80 ms. The real cause is in `swapHarness.restart()`
(`internal/fleetlink/client_test.go`), which closed the old `Hub` *before*
swapping in the new one:

1. `Hub.Close` sends bye, and the client redials after `backoff(1)` (≤10 ms).
2. If that redial lands in the gap, `Serve` on the closed hub answers 503 with
   `Retry-After: 30` (the `Config.RetryAfter` default).
3. The client honours that, waiting `max(30 s, backoff)`, so the 2 s `waitFor`
   can never be met.

A loaded runner widens the gap.

**Fix.** Test-only. `restart()` now swaps the new hub in under `h.mu` first,
then closes the old one, and the comments say why. There is no product-code
change, and production behaviour is unchanged: a shutting-down hub's 503
costs `RetryAfter` by design (ADR-0114 §4).

## Verification

- **Deterministic repro.** A 50 ms sleep between `old.Close()` and the swap
  in the original code fails 5/5 with the exact CI message. The same sleep
  between swap and close in the fixed code passes. The reviewer re-ran both
  independently and got the same result.
- **Card AC.** `go test ./internal/fleetlink/ -race -cpu 1 -count=200 -run
  'TestClient_StatusFollowsTheLinkLifecycle$'` passed 4 × 200 alongside
  6 CPU-burning processes on 4 cores.
- **Gate.** `go build ./...`, `go vet ./internal/fleetlink/`, `gofmt -l .`
  (clean), `golangci-lint run ./...` (0 issues), and
  `go test ./internal/fleetlink/ -race -count=3` (ok).

## Independent review

Reviewer: Fable 5.1 subagent. The author is Opus 5.5.

- **Verdict:** safe to merge, with no blockers or majors.
- **Checked:** the root cause, both `restart()` callers, every `h.hub` access
  (all under `h.mu`, `-race` clean), and that the sibling bye race below
  cannot fail the card's test (`markLinkUp` zeroes `lostAt` before `Linked`
  turns true).
- **Nit, fixed:** the `restart()` comment's wording was ambiguous.
- **Nit, accepted:** the 503 / Retry-After behaviour is by design.

## Deferred

**ut-docs#3571.** The sibling `TestClient_ReconnectsAfterMainTillRestartWithoutCountingAFailure`
also flakes under load on `main`, with "a bye counted as a failed contact".
Instrumentation shows a production race: the replica's writer hits the
socket the main till dropped after its bye, and closes the conn before the
reader consumes the queued bye. `classify()` then reports `endLost`.

That needs a product-code change, so it is filed as its own card rather than
widening this test-only fix. It is not caused or worsened by this change, and
it reproduces on unmodified `main`.
