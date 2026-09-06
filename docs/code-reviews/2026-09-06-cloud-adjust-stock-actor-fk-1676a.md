# Code review: `cloudAdjustStock`'s audit actor "cloud" violates a real FK

**Date:** 2026-09-06
**Branch:** `fix/1676a-cloud-adjust-stock-actor-fk`
**Card:** found while scoping ut-docs#1657/#1676; split out as its own
standalone fix per independent review's recommendation (below)
**Complexity:** easy (one-line production fix + a new regression test)
**Dev model:** Sonnet (inline) · **Review model:** Opus (subagent)

## What shipped

`internal/pages/cloudsync_wire.go`'s `cloudAdjustStock` — the handler for
the cloud's `adjust_stock` directive (`StartCloudSync`'s `AdjustStock`
hook) — recorded its audit-log entry with `ActorID: "cloud"`. Real
migrations (`internal/db/migrations/001_init.sql:359-360`) declare
`FOREIGN KEY (actor_id) REFERENCES users (id)` on `audit_log`, and
`"cloud"` was never a seeded user. `RecordStockMovement`'s audit insert
runs inside the same transaction as the stock movement itself
(`internal/data/pos_repo.go:554-563`), so the FK violation rolled back
the *entire* movement and `cloudAdjustStock` returned an error — **every
cloud `adjust_stock` directive has been failing outright in production**,
silently, since this shipped.

Fix: use `"system"` — the established id for this exact situation (see
`sync_orders.go`'s own `auditActorID` comment: *"audit_log.actor_id has
a users-FK a till name/id would violate"*). Also prefixed the recorded
reason with `"cloud: "` so the adjustment's cloud origin stays visible
in the audit payload's `reason` field now that the actor id can no
longer carry it.

## How this was found

While scoping ut-docs#1657 (swap `internal/pages`' test-DB fixture from a
hand-rolled schema copy to real migrations, ut-docs#1676), running the
package's full test suite against the real schema surfaced this as one
of several genuine production bugs the old fixture's lack of FK
enforcement had been masking.

## Independent review (Opus)

Full review covered the larger #1676 branch this was originally
committed on; the relevant findings for this specific fix:

- **Revert-verified.** Reverted `ActorID: "cloud"`, ran
  `TestCloudAdjustStock_*` — all three existing tests failed with
  `insert audit: constraint failed: FOREIGN KEY constraint failed`.
  Restored the fix — all pass again. Confirmed the bug is real: *"Every
  cloud `adjust_stock` directive has been failing outright in
  production."*
- **`"system"` is the right choice, not a dedicated `"cloud"` seed row.**
  `001_init.sql` only runs on fresh installs — adding a `cloud` row
  there wouldn't reach already-migrated tills, and a proper fix would
  need a new migration (out of scope for a one-line actor fix). Grepping
  `ActorID:\s*"` across non-test `internal/` code turned up exactly two
  other hardcoded actor ids, both `"system"`/`"kiosk"`, both real seeded
  users — `"cloud"` was the sole outlier.
- **Auditability caveat**, addressed in this version: the review noted
  that `RecordStockMovement`'s payload (`{type, quantity, reason}`) only
  said "cloud" when the caller passed an empty reason, so any real
  cloud-supplied reason left no trace of the adjustment's origin. Fixed
  by unconditionally prefixing `reason` with `"cloud: "`.
- **Sequencing recommendation, followed.** The review's overall verdict
  on the larger #1676 branch was "not safe to merge as-is" — landing the
  full test-DB swap there would take `internal/pages` from green to 440
  failures (the fallout tracked in ut-docs#1677/#1678/#1679, plus two
  new cards it surfaced, #1681/#1682) and turn `main`'s CI red. It
  explicitly recommended landing this one-line production fix
  *separately*, with its own regression test that doesn't depend on the
  swap — which is this PR.

## Verified beyond automated tests

- New regression test (`TestCloudAdjustStock_AuditActorSatisfiesRealForeignKey`)
  opens a real migrated database directly via `internal/db.Open`,
  independent of `internal/pages`' own test fixture (`openPagesTestDB`),
  so it stays a true regression test regardless of that fixture's schema
  and doesn't wait on ut-docs#1676's swap to land.
- `go test ./internal/pages/ -timeout 300s` — full package green
  (115.6s), confirming this fix alone introduces no fallout.
- `gofmt -l`, `go vet ./internal/pages/...`, `golangci-lint run
  ./internal/pages/...` (0 issues), `scripts/ci/guard-data-access.sh` —
  all clean.
- Confirmed the `"cloud adjustment"` default-reason assertion in the
  pre-existing `TestCloudAdjustStock_DefaultsReasonWhenEmpty` still
  passes with the new `"cloud: "` prefix (`strings.Contains` on a
  substring, not an exact match).

## Verdict

Safe to merge. Small, precedented, TDD-verified, real production
correctness fix; no scope creep.

## Deferred (tracked separately, not this PR)

- ut-docs#1677/#1678/#1679/#1681/#1682 — the test-DB-swap fallout and
  two more production/test findings from the same investigation, staying
  on `fix/1676-pages-test-db-real-migrations` until that branch is
  green.
