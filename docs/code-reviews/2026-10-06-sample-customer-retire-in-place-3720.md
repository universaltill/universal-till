# Review — sample-customer exclusion: retire-in-place branch coverage (ut-docs#3720)

**Date:** 2026-10-06 · **Lane:** `lane:cloud-54` · **Complexity:** easy
**Author model:** Opus 5.5 · **Reviewer model:** Fable (independent subagent)

## What shipped

Test only — `internal/data/customer_erased_3435_test.go`:
`TestApplyAdminWithResult_RetiredSampleCustomerIsNotAnErasure`. Follow-up from
the ut-docs#3435 review (`2026-10-05-customer-erased-plugin-event-3435.md`):
the existing sample-data tests only exercised `deleteMissing`'s hard-delete
branch. The new test seeds demo customers on the primary, syncs them, pins one
with a replica-local `sales` row (FK-blocked → retire-in-place), removes the
sample data and erases a real customer on the primary, then asserts:

- the pinned demo customer survives as a scrubbed shell (`name = ''`) — proof
  the retire branch ran, not hard-delete;
- `ErasedCustomerIDs` is exactly `[c-real]`;
- re-applying the same bundle reports nothing.

No production code changed.

## Verification

- Passes against `main`'s code (correctness was already established by code
  reading in the #3435 round-2 review).
- Mutation 1: exclusion disabled (`true || !sampleCustomers[sid]` in
  `sync_admin_repo.go`) → fails: `erased customers = [c-real cust-001
  cust-002 cust-003], want [c-real]`.
- Mutation 2: pinning sale removed → fails at the retire-shell assertion
  (`sql: no rows in result set`), so the test can't silently fall back to the
  hard-delete branch.
- Both mutations reproduced independently by the reviewer in a scratch copy;
  reviewer also ran `-count=5` (deterministic).
- `gofmt`, `go vet ./internal/data`, `go build ./...`, `go test ./internal/data`,
  `guard-data-access.sh`, `guard-core-neutral.sh`, `guard-card-data-schema.sh`
  clean. `golangci-lint` / `guard-deadcode-baseline.sh` could not run locally
  (tool binaries built with Go < 1.27); a test-only file can't change their
  result — CI runs them.

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| nit | Doc comment over-attributed the protection to the snapshot's timing; for a retired row the `is_sample_data=1` flag survives, so what the test pins is the filter applying to retire-branch ids | Fixed — comment reworded |
| nit | First pull's result discarded rather than asserted empty | Accepted — mirrors the sibling sample-data test; not part of the AC |

No blocker/major/minor findings. No UI surface, no locale keys, no help topic
affected.

## Verdict

Safe to merge.
