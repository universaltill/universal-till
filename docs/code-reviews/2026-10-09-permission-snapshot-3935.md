# Review: one permission-generation read per request (ut-docs#3935)

Date: 2026-10-09 · Lane: lane:cloud-41 · Author: Sonnet (dev subagent) · Reviewer: Opus 5.5 (independent subagent, fresh context)

## What shipped

- Before this change, `AuthRepo.HasPermission` answered from the #3166
  bitmask but read `sync_admin_version.generation` on every call. A menu
  render calls `canPerform` about 10 times, so that was about 10 reads.
- **Per-request snapshot.** `data.WithPermissionSnapshot(ctx)` adds an
  empty slot (`atomic.Pointer[permBitmask]`) to the request context.
  - `auth.Middleware` attaches the slot for every resolved cookie session.
  - The request's first check reads the generation and stores the current
    bitmask in the slot. Every later check answers from the slot with no
    query.
- **No time-based reuse,** as the card requires. The snapshot ends with
  the request, so a revoke applies on the next request.
- **Same-request writes.** `SetRolePermission`, `UpsertCloudRoleTx`,
  `ReplaceRoleGrantsTx` and `DeleteCloudRoleTx` empty the slot, so a
  later check in the same request reads again.
- **No generation row.** The row-lookup fallback never fills the slot.
- **Safe sharing.** A published `permBitmask` is never mutated (a rebuild
  makes a new one), so the pointer is shared without a lock.
- **Locking.** The rebuild moved into `currentPermBitmask` with a
  `defer Unlock`.
- Docs: `docs/performance.md` has a new section. ADR-0128 §6 still holds:
  the cache still follows the generation, and this only bounds how often
  the generation is read.
- No migration, no UI, no i18n, no help-manual change, because nothing
  visible to a shop changes.

## Tests

`internal/data/auth_permission_snapshot_3935_test.go` uses a counting
`driver.Connector` that counts SELECTs mentioning `sync_admin_version`:

- `OneGenerationReadPerRequest`: 10 checks across roles and actions make
  1 read, and every answer matches the row-lookup oracle.
- `RevokeAppliesOnNextRequest`: a new snapshot sees a revoke.
- `OwnWriteVisibleInSameRequest`: `SetRolePermission` and then a check
  in the same ctx sees the write, with 2 reads.
- `CloudRoleWritersDropSlot`: each of the three `*Tx` writers empties a
  filled slot. This test was added after the review.
- `NoGenerationRowNeverCached`: the fallback follows the rows on every
  call.
- `NoSnapshot_ReadsGenerationEveryCall`: without a slot, the old shape is
  kept.

`internal/auth`: `TestMiddleware_ResolvedSessionCarriesPermissionSnapshot`
makes two `Can` calls inside `next`, with a revoke that bumps the
generation in between. The second call still answers from the snapshot.

**TDD, red then green.** The reviewer re-ran each removal in its own
worktree, and each made its test fail:

| Removed | Failing test | Message |
|---|---|---|
| The slot (`WithPermissionSnapshot` returns ctx unchanged) | `OneGenerationReadPerRequest` | "read the generation 10 times, want 1" |
| The middleware change | `ResolvedSessionCarriesPermissionSnapshot` | "snapshot missing from request ctx" |
| The drop in `SetRolePermission` | `OwnWriteVisibleInSameRequest` | — |
| The drop in `UpsertCloudRoleTx` (I checked this one myself) | `CloudRoleWritersDropSlot/UpsertCloudRoleTx` | — |

## Findings

The reviewer checked these security paths and found nothing:

- **Snapshot outliving a request.** The slot exists only in the ctx built
  for a cookie session.
  - The SSE loop in `order_status.go` and the `fleetlink` websocket make
    no permission checks.
  - The `WithoutCancel` jobs never call `HasPermission`.
  - The till-to-till endpoints (`sync_catalog.go`, `sync_settings.go`)
    get no slot.
- **Elevation.** The snapshot holds every role's mask, so the approver's
  role is checked correctly.
- **Writers outside `AuthRepo`.** The bundle and directive apply runs from
  the background sync tick, never inside a request.
- **Races.** The `-race` run was clean.

Other findings and what happened to them:

| Severity | Finding | Outcome |
|---|---|---|
| Low | The slot is dropped before the caller commits. A check between the write and the `Commit` would re-fill the slot from the pre-commit grants. | No caller does this today. The `HasPermission` doc comment now forbids it. |
| Low | The three `*Tx` writers had no test for the slot drop. | Fixed: `CloudRoleWritersDropSlot`. |
| Nit | The unlock was manual, with no `defer`. A panic during the rebuild would leave `permMu` locked. | Fixed: `currentPermBitmask` uses `defer`. |
| Nit | The slot key is per ctx, not per `AuthRepo`. | Correct, because every repo reads the same till DB. Now documented. |
| Nit | `RevokeAppliesOnNextRequest` would also pass without the change. | Kept: it guards against a future global cache. |

## Verified

- The reviewer ran `go build`, `go vet`, and `go test -race` on
  `internal/data`, `internal/auth` and `internal/httpx`, plus the
  `internal/pages` authz/permission/menu/rail tests. All passed.
- The final full gate (gofmt, build, vet, golangci-lint, the guards and
  `go test ./...`) ran on the final tree before the push.
- Not driven in a browser: nothing visible changes.
- Not added: a pages-level menu-render query-count test. The data-level
  count and the middleware behaviour test cover the contract between them.

**Verdict: safe to merge.**
