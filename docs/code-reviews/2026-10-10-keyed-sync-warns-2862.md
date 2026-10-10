# Review: keyed sync WARNs clear on recovery (ut-docs#2862)

Branch `fix/2862-keyed-sync-warns`. Card: complexity:easy. Built by a Sonnet
dev subagent; independent review by an Opus 5.5 subagent in a separate
worktree; review fixes by the orchestrator (Opus 5.5).

## What shipped

The Problems ring (`internal/logging`) feeds the cloud heartbeat's
"Attention needed". Keyed problems stay open until `ResolveProblems(key)`;
unkeyed ones age out after 24h. Several sync WARNs had no paired recovery
and stayed listed after the condition was over.

- `logging.HasOpenProblem(key)` and `logging.ResolveProblemsWhere(match)`.
- **Pairing revoked** (`OnRevoked` → `linkPairingRevoked`): keyed
  `sync.link_pairing_revoked`, resolved in `primaryContactOK` when the main
  till accepts this till again.
- **Plugin sync** (`convergePluginSet`): broken re-fetch, install and
  uninstall failures are keyed per listing (`plugin_sync.{broken,install,uninstall}:<listing>`).
  Each one WARNs once while its key is open, and repeats are logged at INFO.
  A key is resolved on its success branch. A sweep also closes the keys of
  listings the primary dropped, and the uninstall keys of listings that are
  no longer a pending uninstall. New seam: `pluginSyncRemove`.
- **Refused proof** (`discovery.rediscover`): no longer shares the outage key
  `sync.main_till_contact`. Unkeyed, it ages out 24h after it was last seen,
  so a contact with the real main till no longer silently clears a warning
  about a stale or spoofing device. A per-address key was rejected: rediscover
  only runs during an outage, so "the address disappeared" can't be observed
  and the problem would stay open forever.
- **Promote** (`POST /api/sync/promote`) resolves all of the above
  (`resolveReplicaSyncProblems`), because the replica's pull loop and link,
  the only resolvers, stop for good.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | After promote, the pull loop and link never run again, so the revoked, plugin-sync and outage keys stayed open forever. Before this change they aged out after 24h. | **Fixed**: `resolveReplicaSyncProblems` in the promote handler. Test `TestSyncPromote_ResolvesTheReplicasSyncProblems` failed first: all 5 still reported. |
| 2 | major | An uninstall key whose install record went away or left Active by another path (cloud directive `ClearForPlugin`, Failed install) never resolved. | **Fixed**: the sweep resolves uninstall keys that are not pending, but only when the records list was read OK. Test `TestConvergePluginSet_UninstallKeyClearsWhenItsRecordGoes` failed first: still reported. |
| 3 | minor | A successful re-fetch resolves the broken key in the same tick, so a plugin that re-breaks on reload WARNs per attempt. | **Accepted, comment corrected**: the attempts are bounded by `shouldRefetchBroken`'s backoff, which ERRORs once the burst is spent. No regression. |
| 4 | minor | A pull success resolves the revoked key, but `fleetlink` never re-dials a revoked target until the URL or bearer changes, so the link stays off silently after a transient 401/403. | Older behaviour, not introduced here. Filed as ut-docs#4044 (Backlog). |
| 5 | nit | `ResolveProblemsWhere` runs `match` under the ring lock. | Doc comment now says `match` must not log. |
| 6 | nit | `HasOpenProblem` → `WarnProblemf` race. | Benign: the pull tick is single-goroutine, and at worst one duplicate line is closed by the same resolve. |

## Verified

- The reviewer re-ran each TDD claim (revert → fail → restore → pass):
  - `TestPairingRevoked_ClearsWhenTheMainTillAcceptsTheTillAgain`
  - `TestConvergePluginSet_InstallFailureWarnsOnceAndClearsOnSuccess`
  - `TestPrimaryWatch_RefusedProofWarnsUnkeyed` and `TestPrimaryWatch_CloudRefusedProofWarnsUnkeyed`

  Each failed with the claimed message.
- The two review-fix tests were written first and seen failing with the real error.
- Gate: `gofmt`, `go build ./...`, `go vet`, `go test ./...`,
  `golangci-lint`, and the ci.yml `build` guards. Results are on the PR.
- No file writes, no paths, and no SQL outside `internal/data`. Backend
  only: no UI, i18n or help surface changed. The log lines are existing
  English diagnostics.

## Verdict

Safe to merge once CI is green.
