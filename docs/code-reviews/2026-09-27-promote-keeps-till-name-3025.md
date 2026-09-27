# Code review — a promoted replica keeps its own till name (ut-docs#3025)

**Date:** 2026-09-27 · **Lane:** lane:cloud-24 · **Complexity:** easy
**Author:** Sonnet (dev subagent) · **Reviewer:** Opus 5.5 (fresh context, one pass, separate worktree)

## What shipped

`POST /api/sync/promote` calls `SettingsRepo.ClearReplicaIdentity`, which deleted every `sync.*` key except `sync.receipt_prefix`. That included the replica's own name, `sync.till_name`. `till.name` is shop-wide (`data.ShopWideSettingPrefixes`), so on a replica it holds the old main till's name. Since #3019, a main till reports `till.name` to the cloud (`enroll.DeviceName`). So a replica named "Back Office" in a shop whose main till was "Front Counter" reported itself as "Front Counter" once promoted, and its own name was lost.

- `internal/data/settings_repo.go`: `ClearReplicaIdentity` now runs in one transaction. It reads `sync.till_name`, and if the value is non-blank it upserts the trimmed value into `till.name`. Then it runs the existing DELETE and commits. A failure anywhere rolls back the whole promote. This follows the Architect's option (a) on the card.
- `internal/enroll/replica.go`: comment only. `DeviceName`'s doc now notes that promotion copies the name across.
- Tests:
  - `TestSettingsRepo_ClearReplicaIdentityKeepsOwnTillName` covers four cases: the name is copied, a missing name leaves `till.name` alone, a whitespace-only name leaves it alone, and whitespace is trimmed.
  - `TestSyncPromote_KeepsOwnTillName` goes through the real mux. It checks `till.name`, that the `sync.*` keys are cleared, and `enroll.DeviceName`.
- ut-docs `architecture/lan-sync.md` §"Promoting a replica" now says promotion keeps the till's own name (separate ut-docs PR).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A replica that joined with a blank name (the main till records it as "till") has an empty `sync.till_name`. Promoting it skips the copy, so `till.name` still holds the old main till's name. This is the same failure as the card, in a narrower case. | Deferred to ut-docs#3030. The fix needs an Architect call: clear `till.name`, or store the name the main till assigned at join. |
| 2 | minor | `architecture/lan-sync.md` didn't describe the new behaviour. | Fixed in the ut-docs PR. No `web/help/` topic walks through promotion, so there is no help change. |
| 3 | nit | Direct `== sql.ErrNoRows` comparison. | Kept. It matches `Get` in the same file, and `Scan` returns the sentinel unwrapped. |
| 4 | nit | The `till_promoted` audit row doesn't record the name change. | Accepted. The name comes from the till's own join and isn't a new operator input. |

The reviewer also checked these and found no problem:
- **Transaction:** `_txlock=immediate` and `busy_timeout`, and the handler holds no outer tx, so there is no deadlock risk.
- **Error tracing:** `err` is reused, not shadowed, so the deferred trace sees the error.
- **Other callers:** `/api/sync/promote` is the only production caller.
- **In-memory caches:** nothing caches `till.name`. `settings.Store` is a pass-through, and the name is read from the DB per request or per tick.
- **Other tills:** re-paired replicas now pull the promoted till's name as the main till's name, which is the intended behaviour. The enrolment name-uniqueness check compares against it too.
- **Repository pattern:** all SQL stays in `internal/data`.

## TDD re-verification (reviewer, separate worktree)

With only the production files reverted, the new tests fail on the real bug:
- `small_repos_test.go:717: expected till.name = "Back Office", got val="Front Counter"`
- `small_repos_test.go:765: expected till.name = "Back Office" (trimmed), got val="" ok=false`
- `sync_api_test.go:1749: expected till.name = "Back Office" after promote, got val="Front Counter"`

The "missing" and "whitespace-only" subtests pass on both versions. They are negative controls. Every test passes again once the fix is restored.

## Gates

- `gofmt -l internal/` printed nothing.
- `go build ./...` and `go vet` on data, pages and enroll are clean.
- `go test ./internal/data/ ./internal/enroll/ ./internal/pages/` passes.
- `guard-data-access.sh` and `guard-core-neutral.sh` pass.

## Not verified

- No UI surface changed, so there are no screenshots.
- I didn't do a driven two-till promote on real devices. The handler test goes through the real mux and a real migrated DB.

**Verdict:** safe to merge.
