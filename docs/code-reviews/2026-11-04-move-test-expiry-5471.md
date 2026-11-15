# Review: #5127 move tests hardcoded an expiry that has now passed (ut-docs#5471)

Branch `fix/5471-move-test-expiry`. Test-only, three files. The fix was written
by the orchestrator, an Opus build cycle, after `main`'s `ci` went red at
`eeb9a26`. That merge only touched JS; the same failure reproduces at
`f3d6a7a`, which was green at 08:56Z. `[no-subagent: test-only fix in three
lines, cause verified directly]`. The independent review was done by Sonnet,
a different model.

## What shipped

The move offer expiry in the ut-docs#5127 tests was the literal
`2026-11-03T09:30:00Z`. Once `now > ExpiresAt + pendingMoveGrace`
(`internal/enroll/move.go:670`), every pending move counted as expired:
- in `internal/pages`, "This move is no longer waiting for approval" was shown;
- in `internal/enroll`, the move was "not held";
- in `internal/cloudsync`, the moves were dropped.

The expiry is now `time.Now().UTC() + 24h` (RFC3339) in each file:
- `internal/pages/move_approval_5127_test.go` (`mvExp`);
- `internal/enroll/move_5127_test.go` (`moveExp`; the swapped record is
  +72h, so it still differs from `moveExp`);
- `internal/cloudsync/pending_moves_5127_test.go`.

## Verification

- **Red:** main `ci` run 41808211773 failed in `enroll` (9 tests),
  `cloudsync` (`TestPushSyncCarriesPendingMovesOn200And402`) and `pages`
  (10 move tests). The local `go test ./internal/pages -run 'TestMove|TestEnrolPair_Move'`
  fails at both `f3d6a7a` and `eeb9a26`.
- **Green:**
  - `go test -count=1 ./internal/enroll ./internal/cloudsync ./internal/approvalkey`: ok.
  - `go test -count=1 ./internal/pages`: ok (full package).
  - `gofmt -l internal`: empty.
  - `go vet`: clean.

## Review findings (Sonnet)

- APPROVE, nothing blocking.
- A package-level `time.Now()` var is computed once per binary, so
  exact-string and `ExpiresRaw` round-trip assertions stay consistent. The
  value is 20 characters, under the 40-character check at `move.go:523`.
- Other dated literals were checked and are not time bombs:
  - `usable_from` 2026-11-05 is echoed only;
  - `approvalkey_test.go:148` is a tamper mutation;
  - `entitlement.KeyExpiresAt` is display-only.

Pre-flight: test-only; help/reference/README/en.json/ADR n/a.

## Deferred

- A guard that flags RFC3339 literals used as expiry or deadline values in
  `_test.go` files. Noted on ut-docs#5471 as a follow-up idea.

Verdict: merge.
