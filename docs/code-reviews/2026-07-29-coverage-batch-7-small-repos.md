# Test coverage batch 7: remaining small internal/data repos

2026-07-29

Seventh batch: the last standalone zero-coverage functions scattered across
several small, single-purpose repos — `HeldSalesRepo` (parked in-progress
sales), `TillsRepo` (enrolled replica tills, ADR-0011), and one gap each in
`InstallStatusRepo`, `ModifierRepo`, `RelatedItemsRepo`, `SettingsRepo`, and
`InvoiceRepo`.

## What changed

`internal/data/small_repos_test.go` (new). Notably covers
`SettingsRepo.ClearReplicaIdentity` — the primary/replica-promotion flow
(ADR-0011 D4) that clears an old primary's sync identity while
deliberately preserving `sync.receipt_prefix`, so a promoted replica's
receipt numbering never collides with the old primary's. Getting this
backwards (clearing the prefix instead of preserving it) would be a real,
silent data-integrity bug — receipt numbers colliding across tills. The
test seeds multiple `sync.*` keys plus the prefix plus a non-sync key, and
asserts all four independently, so it can't pass by getting the exclusion
logic backwards or dropped.

Also covers `TillsRepo.TillByBearerHash`'s heartbeat side effect (stamps
`last_seen_at` on every successful lookup — a replica's sync auth call
doubles as a liveness signal) and `InvoiceRepo.List`'s bare-date `to`
bound (a `￿` sentinel appended so a date-only bound includes the whole
day, not just up to midnight).

## Independent review (opus)

Verified all four higher-risk assertions (`ClearReplicaIdentity`'s
exclusion logic, the heartbeat side effect's ordering, the date-sentinel
inclusion, and hand-rolled test-schema fidelity against the real
migrations) genuinely prove what they claim rather than passing by
coincidence — specifically confirmed each would fail if the underlying
logic were inverted or removed. No findings, nothing blocking.

## Verification

`go build ./...`, `go test ./...`, `go test ./internal/data/... -count=3
-shuffle=on`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

## Coverage delta

`HeldSalesRepo` and `TillsRepo`: 0% → covered (both fully). Plus one gap
each closed in `InstallStatusRepo`, `ModifierRepo`, `RelatedItemsRepo`,
`SettingsRepo`, `InvoiceRepo`. This closes out the "remaining small
internal/data repos" batch — combined with batches 1-6, every repo in
`internal/data` now has meaningful coverage (auth_repo.go was already
well-covered indirectly via internal/auth's own suite, confirmed before
starting this batch).
