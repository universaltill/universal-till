# Review: held sales — atomic local-only fallback, claimed re-park demotes (ut-docs#3034)

Date: 2026-09-27 · Branch: `fix/3034-held-sale-local-only-atomic` · Lane: `lane:cloud-24`
Author: Sonnet dev subagent (card `complexity:easy`). Reviewer: Opus 5.5, fresh context, in a separate worktree.

## What shipped
- `HeldSalesRepo.UpsertLocalOnly`: the same statement as `Upsert`, except that `primary_synced` is 0 on both the insert and the update branch. Row write and demotion happen in one statement.
- `MarkLocalOnly` is removed. Its only caller was the #2723 give-back, which ran `Upsert` and then a separate `MarkLocalOnly`. Its test was converted to cover `UpsertLocalOnly`.
- `pos.HeldOrigin.Claimed` is new. `resumeHeldSale` sets it from `heldSaleClaimForResume`'s `claimed` result.
- `heldSaleWriteThrough(..., claimed bool)`: on the `!ok` (local-only) fallback it uses `UpsertLocalOnly` when `claimed` is true, and plain `Upsert` otherwise. The refused (`!applied`) branch is unchanged, because a refusal proves the primary holds the id (Amendment A F11).
- Callers:

  | Caller | `claimed` |
  |---|---|
  | `heldSaleGiveBack` | `true` |
  | `parkCurrentBasket` | `origin.Claimed` (false on a first park) |
  | table move | `false` |
  | kiosk counter checkout | `false` |

## Tests (TDD; the reviewer re-verified the red runs)
- `TestResumeOnReplica_GiveBackNeverCommitsAnIntermediateConfirmedMirror`
  - A test-only SQLite trigger logs every commit that leaves the id at `primary_synced=1`.
  - Old code: 1 such commit, so the test fails. New code: 0.
- `TestResumeOnReplica_ReparkOfClaimedIDDoesNotStickPrimarySynced`
  - A `BEFORE DELETE` trigger makes the resume's local delete fail. The re-park then runs with the primary's `/upsert` returning 500.
  - Old code: `PrimarySynced:true`, so the test fails. New code: the row is 0 and survives `ReconcileWithPrimary(nil)`.
- A guard test covers a non-claimed re-park during an outage; it keeps the MAX-sticky `Upsert`. The data-layer tests cover `UpsertLocalOnly` for insert, for demote-on-update with `created_at` kept, and confirm that `Upsert` still never lowers the flag.

## Findings
1. **Minor — fixed.** The `HeldOrigin.Claimed` and `parkCurrentBasket` comments said the claimed fallback also covers "its own upsert refused". It does not, by design. Both comments now say that a refusal stays a confirmed mirror.
2. **Minor — pre-existing, filed as ut-docs#3038.** `ReconcileWithPrimary` marks ids synced from a list fetched *before* its transaction. A render that fetched the list before this till claimed the order can therefore raise a given-back row back to 1. The doc wording that claimed "nothing can ever observe it at 1" was toned down to what the single statement actually guarantees.
3. **Nit — fixed.** This review record was missing.

The reviewer confirmed the following:
- Every caller passes the right `claimed` value.
- A resume that fell back to a local mirror while the primary was unreachable has `claimed=false`, so a mirror the primary still holds is never demoted.
- The standalone/primary till path is harmless: rows there are 0 anyway.
- `Claimed` is cleared with `heldOrigin` on every reset path and is never persisted.
- Checked for file writes without `os.MkdirAll` and for paths relative to the working directory: none, since the diff has no file I/O.

## Gate
- `gofmt -l .`: no output.
- `go build ./...`: ok.
- `go vet`: ok.
- `golangci-lint run ./...`: 0 issues.
- `guard-data-access.sh`: pass.
- `go test ./internal/data ./internal/pos -race`: ok.
- `go test ./internal/pages`: ok. Run without `-race`, because the full package under `-race` exceeds the sandbox's time limit.
- `go test ./internal/pages -race -run 'Held|Hold|Resume|Park'`: ok.

Not run: the Playwright e2e suite. This is a backend change to the replica's local-only fallback, and it has no visible surface.

## Verdict
Safe to merge.
