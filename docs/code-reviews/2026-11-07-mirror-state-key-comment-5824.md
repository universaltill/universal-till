# Review: MirrorStateKey doc comment matches the three-phase cursor (ut-docs#5824)

Date: 2026-11-07 · Lane: `lane:cloud-41` · Complexity: easy

## What shipped

Comment-only change in `internal/data/sales_mirror_repo.go`. The
`MirrorStateKey` doc comment said a state re-sync pages sales in
`(local_date, id)` order and then closes, with phase "sales" or "closes".
It now says what the code does:

- `sales` (`StateSaleIDs`) in `(local_date, rowid)` order;
- `closes` (this main till's own, `StateCloseIDs`) in `(created_at, id)` order;
- `peer` (other tills' closes, `StatePeerCloseKeys`) in `(created_at, rowid)` order;
- A/B hold the last position served, exclusive: B is a base-10 rowid for
  sales and peer, the `report_archive` id for closes; an empty A starts the
  phase at its beginning.

This matches `reference/design-5074-mirror-sales-to-replicas.md` §4.4 in ut-docs.

Process: one comment-only edit, so BA, Architect and Dev ran inline in the
orchestrator `[no-subagent: one comment-only edit]`. The review was done by an
independent Sonnet subagent, a different model from the author (Opus).

## Review findings

The reviewer checked every claim against `sync_sales_mirror.go` (the phase
switch at :541/:597/:627, cursor values at :594/:623/:660, and
`decodeMirrorStateCursor` at :473-486) and against the repo SQL (orderings,
strict-after filters, and the `afterDate < fromDate` clamps).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | Naming the unexported `decodeMirrorStateCursor` in another package could go stale | Accepted. The name is accurate today, and a rename would show up in a grep. |
| 2 | info | The empty-A behaviour also holds when B is non-empty | Accepted. The comment makes no claim about B in that case. |

No blockers. Verdict: **approve**.

## Verification

- `gofmt -l internal/data`: clean.
- `go build ./...`: OK.
- `go vet ./internal/data/ ./internal/pages/`: OK.
- `go test ./internal/data/ -run Mirror -count=1`: ok.
- There is no TDD claim to re-verify because the change is comment-only.

**Pre-flight:** review record — updated (this file); web/help topic — n/a (no user-visible behaviour); `reference/` page — n/a (design-5074 §4.4 already correct, ut-docs#5822); `en.json`/meta — n/a; README — n/a; follow-ups — none.
