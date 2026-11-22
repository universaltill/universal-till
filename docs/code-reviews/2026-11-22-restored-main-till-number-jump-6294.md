# Review: a main till restored as a whole data folder jumps its receipt/Z numbers (ut-docs#6294)

Date: 2026-11-22. Lane `lane:cloud-41`. Follow-up of the #5068 round-2 review, finding R6.

## What shipped

- `internal/enroll/restored.go`: `CheckRestoredInstall` takes an `owe` callback
  (same pattern as the journal lookup, so `enroll` imports neither `db` nor
  `data`). On a detected restore of a **main** till (no `sync.primary_url`),
  in both the normal and the env-pinned branch, it records the jump as owed
  with the stable token `restored-install:<DB install id before re-mark>`,
  after the identity reset and **before** the re-mark. A failed write logs
  and skips the re-mark, so the next boot detects the restore again and
  retries with the same token. Boot is never blocked. A replica owes nothing
  (its provisional receipt prefix already covers it).
- `internal/db/backup.go`: `OweNumberJump(dbPath, token, applied)` writes the
  existing `restore-numbers-pending` marker exclusively (fsync; a partial file
  is removed). An existing marker is kept (a staged restore that has not
  jumped yet wins, one jump covers both), unless its token is the one the DB
  already jumped under: that spent marker is replaced atomically (temp file,
  fsync, rename). The staged-restore writer is unchanged.
- `internal/data/restore_numbers.go`: `AppliedRestoreJumpToken` (read-only).
- `internal/app`: `bootRestoreChecks` runs the restore check, then the single
  jump call (moved from before the check). Nothing between the old and new
  position issues or reads receipt/Z numbers or floors; the jump still runs
  before `LoadRuntimeConfig`.
- Help: `web/help/en/backups.md`, one English sentence; docs-shots manifest
  regenerated (no screenshot changed).

## TDD

Dev's first fail-before was compile-only, so the Tester proved it
behaviourally: 12 mutants (no-op owe, jump before the check, fresh timestamp
token in enroll and in db, owe on a replica, re-mark despite a failed owe, owe
after re-mark, overwrite an existing marker, env-pinned branch not owing, owe
only with a cloud identity, old jump call restored) — 11 caught. Survivor:
removing the partial-file cleanup (needs a fault-injecting FS; accepted).
Reviewer re-verified by removing both `oweJumpIfMain` calls: 5 enroll + 4 app
tests failed. Orchestrator re-verified the review fix R1: with the spent-marker
branch disabled, `TestBoot_RestoredStaleMarkerOfAppliedTokenStillJumps`
("stale marker suppressed the jump: audits=0") and
`TestOweNumberJump_ReplacesSpentMarker` fail; restored, both pass.

## Tester

PASS (Opus 5.5). Real binaries from the branch and origin/main, a seeded till
with 8 real sales and Z 1, the whole folder copied and booted 3 times:
- branch, copy without `secrets/` or with another `secrets/install-id`:
  boot 1 receipt 000011001 / Z 4, then contiguous; one jump record.
- branch, normal copy: receipt 9 / Z 2, no jump. Restored replica: provisional
  `R…-` prefix, no marker, no jump.
- origin/main, copy without `secrets/`: receipt 9 / Z 2 — the bug.
The first Z after the jump records it, as the help sentence says.

## Review

- Dev: Sonnet. Independent review: Opus 5.5, fresh context, own worktree.
  Verdict: **ship after fixes**; no blocker or high.
- **R1 (low, fixed):** a restored folder carrying a stale marker whose token
  the original till already applied kept that token, so the jump was a no-op.
  Fixed by the spent-marker replacement above; tests for both the spent and the
  still-pending marker.
- **R2 (low, accepted):** staged restore + detected restore on the same boot,
  and the re-mark's `install_id` write then fails: the next boot re-detects and
  jumps a second time. Only a bigger number gap, never a reused number; needs
  two independent failures. Accepted.
- Nits accepted: `enforceDemoGate` / `ApplyReplicaIdentity` errors now return
  before the jump (boot stops anyway, the marker survives); no parent-directory
  fsync (same as the staged path; a power loss just re-detects).
- Checked sound: boot order, replica detection read after
  `ApplyReplicaIdentity` (a read error counts as main → jumps, the safe side),
  every crash point ends in exactly one jump, marker path next to the DB (dir
  exists, no cwd-relative path), repository pattern, no migration, no i18n key.

Gates: `gofmt -l .` empty, `go build ./...`, `go vet`, `go test ./internal/db/...
./internal/enroll/... ./internal/app/... ./internal/data/...` ok,
`go test ./internal/pages/... -timeout 30m` ok (Tester; the package takes ~633 s
on main too, over go's 10-min default), `golangci-lint run ./...` 0 issues, all
`ci.yml` build-job guards pass except two that cannot run in this container
(`guard-deadcode-baseline`: local deadcode built with an older Go;
`guard-shellcheck-version`: no shellcheck) — left to CI.

Scope note: detection compares the DB install id with `secrets/install-id`, so
a clone or disk image that also copies `secrets/` is not detected (true before
this change too) → follow-up card. Tills restored before this ships were
already re-marked; no backfill (BA default).

Pre-flight: help topic `web/help/en/backups.md` updated; docs-shots manifest
regenerated, no screen change; `reference/` n/a (no page documents the marker);
README n/a; en.json/en.meta.json n/a (no key); ADR n/a (ADR-0166 §D "a restore
still jumps ahead" covers it; no migration, ADR-0100); ut-cloud mirrors n/a;
follow-up: undetected clone with `secrets/` → Backlog card.
