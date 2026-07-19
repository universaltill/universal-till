# Code review — problem digest timestamp precision (2026-07-19)

Branch `fix/problem-digest-timestamp-precision`. Follow-on from the cloud-side
persistent problem history feature (ut-market-place, ADR-0018): an independent
review of that feature caught that its dedup key — `(device, reported_at,
message)` — was weaker than intended because of how this repo serializes the
timestamp.

## Finding

`internal/pages/cloudsync_wire.go:210` (`collectProblems`) formatted the
till's log timestamp with `time.RFC3339` — whole-second precision — before
putting it on the wire, even though the in-memory value (`logging.Problem.At`,
set via `time.Now().UTC()`) is nanosecond-precision. Two genuinely distinct
occurrences of the same recurring message on the same device within the same
wall-clock second (e.g. a fast retry loop) would carry an identical `at`, and
the cloud's dedup would silently coalesce them into one persisted row.

## Fix

`time.RFC3339` → `time.RFC3339Nano`. No wire contract or parsing anywhere
depends on second-only precision — the cloud only stores/sorts/displays `at`
as an opaque string. Old tills keep sending whole-second timestamps (no
regression, same behaviour as today); new tills get full precision.

Build + `internal/pages` and `internal/logging` tests green; data-access
guard green; gofmt clean on the changed file (an unrelated pre-existing
`main.go` gofmt drift on `main` is untouched, out of scope here).
