# Review: LAN sync bearer rotation (ut-docs#5198)

**Date:** 2026-10-30 · **Branch:** `fix/5198-sync-bearer-rotation` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (Dev), from an Opus Architect brief · **Reviewer:** Fable (independent; hard + security card, a different model from the author)

## What shipped

- **Migration 093 (`093_tills_bearer_rotation.sql`):** `tills` gains three main-till-only columns: `bearer_issued_at` (NULL means a legacy bearer), `bearer_prev_hash` and `bearer_prev_expires_at` (NULL means pending). The checksum is pinned. No trigger, so a rotation does not bump the admin generation.
- **`internal/data/tills_bearer_rotation.go` (new):**
  - `TillByBearerHashAt` matches the current or the previous hash. The first use of the new bearer starts the 10-minute grace window (`SyncBearerGrace`). A previous bearer used after expiry is refused and its hash is cleared. `RotateDue` is set for a legacy bearer and for one older than 30 days (`SyncBearerMaxAge`).
  - `RotateBearer` is one conditional UPDATE. Called with the current bearer, it rotates. Called with the previous bearer while still pending (the response to an earlier rotation was lost), it replaces only the unused new bearer.
  - `ProofHashesByID` replaces `BearerHashByID`.
  - `EnrolTill` stamps `bearer_issued_at`. The admin roster sync redacts all three columns, and the join snapshot nulls `bearer_prev_hash`.
- **Main till:**
  - `syncTill(w, r, repo)` sets `X-UT-Sync-Bearer-Rotate: 1` when a rotation is due. The signature change is mechanical across 16 files.
  - New `POST /api/sync/bearer/rotate` (`sync_bearer_rotate.go`):
    - bearer-authed in the handler, exempt in auth middleware, and denied in demo mode;
    - answers 409 on a replica, and 409 when an old bearer whose grace has started tries to rotate;
    - sends `Cache-Control: no-store`.
  - While a rotation is pending, `primary-proof` also returns `prev_proof`, so a replica whose rotate response was lost can still re-find a main till that moved.
- **Replica:**
  - `lantls` `peerRT` (every replica-to-main client) fires `SetBearerRotate`'s hook when it sees the header from the primary host. It never fires for the rotate path itself.
  - The hook runs in a recovered goroutine, single-flight, at most once a minute, so a request and a sale never wait on it.
  - `SyncBearerRotator` posts the rotation, checks the answer is 64 hex characters, re-reads the bearer, and stores it through #5172's atomic `secrets/sync-bearer` write. A failure keeps the old bearer.
- **Docs:** ut-docs `architecture/lan-sync.md` gains a "Sync bearer rotation" section, and `architecture/local-backup.md` is updated. Nothing changes for the owner, so no help topic changes. No ADR: this rotates credentials inside the existing LAN-sync auth (ADR-0011, ADR-0114 §7).

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | medium | Restoring a **main-till** snapshot taken before a till's last rotation brings back the old `bearer_hash` with no previous hash. The replica gets 401 and has to re-join. Before this change a bearer never changed, so that restore kept every pairing. | **Deferred** to ut-docs#5203 (p2). Documented under "Out of scope" in `lan-sync.md`. Main-till restores are rare and the replica's sales stay on the replica until re-join. |
| R2 | low | The rotate POST followed redirects while carrying the bearer. Go keeps `Authorization` on a same-host, other-port redirect. | **Fixed:** the rotate client sets `CheckRedirect = ErrUseLastResponse`, like `sync_link_client.go` and `pin.go`. |
| R3 | low | A replica whose `secrets/` write keeps failing rotates once a minute, forever, logging on both tills. | **Accepted:** bounded by `bearerRotateRetryEvery`. Backoff can come later. |
| R4 | low | The offline-replica test counted served requests but never asserted the count. | **Fixed:** it asserts admin + rotate = 2. |
| R5 | accepted | A stolen stale bearer can rotate on the pending-previous path before the legitimate till uses its new bearer. After the grace window the legitimate till is locked out, which is a DoS a stolen bearer couldn't cause before. | **Accepted residual risk:** documented. A stolen bearer already allowed full impersonation, and the warning on a refused old bearer makes the eviction visible. |

## Verified beyond the automated tests

- **Revert, fail, restore, pass (reviewer):**
  - Disabling the expiry check fails `TestSyncBearerRotate_BackupBearerRefusedAfterGrace` ("old bearer after the grace = 200, want 401").
  - Narrowing the rotate WHERE to the current hash fails `TestSyncBearerRotate_LostResponseReplicaNotLockedOut` ("second rotate: code=409").
  - Running the hook inline fails `TestPeerTransport_RotateHeaderFiresHookAsyncOnce` ("a request waited for the rotation hook").
- **Concurrency probe:** 8 concurrent rotations under `-race` leave exactly one live new bearer, with the old one pending.
- **Persistence path:** the new bearer goes to `paths.Data("secrets","sync-bearer")` through `storeSyncBearer` (MkdirAll, temp + rename, 0600). No path is cwd-relative.
- **Gates:**
  - `gofmt -l`, `go build`, `go vet` and `golangci-lint` (0 issues) are clean.
  - Full `go test ./...` passes (Dev). Affected packages pass again after the review fixes.
  - The `ci.yml` build-job guards pass. The exceptions are environment-only: a local shellcheck 0.11 against the pinned version, and the deadcode toolchain. No shell script changed.
- **`web/help/img/manifest.json`:** the surface hash was refreshed for the `internal/pages` changes. No screenshot changed.
- **Visual check:** none. No UI surface changed.

## Verdict

Safe to merge.
