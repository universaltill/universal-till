# Review: a main-till restore no longer locks joined tills out (ut-docs#5203)

- **Date:** 2026-10-30
- **Card:** universaltill/ut-docs#5203 (follow-up of #5198, found in its review)
- **Branch:** `fix/5203-restore-bearer-fallback`
- **Built by:** Sonnet (Dev subagent, two rounds). **Reviewed by:** Opus 5.5,
  fresh context, in its own worktree; fixes re-verified by the orchestrator (Opus 5.5).

## What shipped

Since #5198 the main till rotates each joined till's LAN sync bearer. A main
till restored from a backup taken before a till's last rotation brought back
that till's old bearer hash, so the till's new bearer got 401 and it had to
join again (wiping its DB).

- **Replica keeps its previous bearer** (`internal/data/settings_sync_bearer.go`):
  `secrets/sync-bearer-prev` (0600, atomic write, outside every backup like
  `sync-bearer`). `RotateSyncBearer` is a compare-and-swap (writes prev = old,
  then current = fresh) and replaces the racy Get-then-Set in
  `pages.SyncBearerRotator`. `AdoptPrevSyncBearer` swaps the two (current =
  prev, then prev = refused). Every other bearer write (join, re-join, unjoin,
  promote via `ClearReplicaIdentity`, `GetOrCreate`, the boot row→file
  migration, empty row included) clears prev.
- **Transport fallback** (`internal/lantls/peer.go`): on a 401 from the main
  till (not the rotate path, not another host, request carried a bearer, body
  replayable), the request is sent once more with the previous bearer. If
  accepted and the adoption actually switched bearers, a rotation is forced
  past the once-a-minute throttle (queued once if one is in flight). A retry
  401 stands; nothing is adopted.
- **Hook** `pages.SyncBearerFallback`, installed in `internal/app/app.go`;
  offers prev only for the bearer that is current right now. No bearer value
  is logged.
- **mDNS re-proof** (`internal/discovery/primary_watch.go`): `challengeVia`
  also accepts a proof keyed by the previous bearer (main till restored on new
  hardware, new address).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | HIGH | A 401 not about the credential (main till's `TillByBearerHashAt` error, e.g. SQLITE_BUSY) inside the grace window made the replica adopt the old bearer and **delete** the live one; forced rotation then got 409 and after the grace the till was locked out. Reproduced end-to-end by the reviewer. | Fixed: adopt swaps instead of deleting, so the next refusal swaps back. Order: current = prev first, then prev = refused — a crash between leaves both = the bearer the main till just accepted (never loses it). Regression `TestSyncBearerFallback_TransientNonAuth401DoesNotLockOut`; re-verified by the orchestrator: with delete-on-adopt it fails (`sync after the grace: 401 -- the replica was locked out`), with the fix it passes. |
| 2 | LOW | Every successful retry forced a rotation, even when its adopt lost the CAS to a concurrent one. | Fixed: `adopt func() bool`; force only when it adopted. Test added. |
| 3 | LOW | Forced rotation dropped when a rotation was already in flight (single-flight); the restored row's recent age then never asks again for ~30 days. | Fixed: `bearerRotateAgain` flag, one extra run after busy is released (checked after release so no request is lost; folds to one run). Test added (failed pre-fix). |
| 4 | LOW | First 401 body drained after the retry, so the retry could not reuse the connection. | Fixed: drained (4 KiB, kept in memory) before the retry; handed back if the retry gets no answer. Tests added. |
| 5 | LOW | "No resurrection" test nulled `bearer_prev_hash`, so the main till's past-grace branch was never exercised. | Fixed: the row keeps the expired prev hash; only `bearer_hash` changes. |
| 6 | V.LOW | Empty `sync.bearer` row migration left a stale prev file. | Fixed + test (failed pre-fix). |
| 7 | info | Requests in flight with the refused bearer while an adoption happens fail once (hook sees current ≠ presented). | Accepted: sync retries them. |

## Accepted residual risk

A copy of a main-till backup taken before a rotation can pass the replica's
proof check for one extra rotation period (until the previous bearer rotates
out). The copy must already be stolen and the attacker must answer the
replica's mDNS lookup on the shop network. #5198's guarantee holds: the main
till decides acceptance, and an expired bearer is still refused
(`TestSyncBearerRotate_BackupBearerRefusedAfterGrace`,
`TestSyncBearerFallback_DoesNotResurrectExpiredBearer`).

## Verified

- TDD re-verified by the reviewer in its worktree: reverting `primary_watch.go`
  fails the prev-proof test; stubbing `SyncBearerFallback` fails both restore
  subtests (`sync after the main till was restored: 401`); unforcing the
  rotation fails the pages e2e and the lantls force test.
- Gate after the last edit: `gofmt -l .` clean, `go build ./...`, `go vet` on
  the touched packages, race runs of `internal/lantls`, `internal/discovery`,
  `internal/data`, `internal/settings`, `internal/app` and the pages bearer
  suite, `golangci-lint run ./...` 0 issues, `guard-data-access.sh` OK.
- `internal/data`'s `TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks`
  timing test fails under `-race` in this container identically on a clean tree
  (pre-existing, unrelated).
- No UI, string, help or migration change. Doc: ut-docs
  `architecture/lan-sync.md` → Sync bearer rotation → "Restoring the main till".

**Verdict:** safe to merge once CI is green.
