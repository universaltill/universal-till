# Review: openPagesTestDB clones the migrated template DB (ut-docs#2219)

**Branch:** `feat/2219-openpagestestdb-template-db`
**Reviewer model:** Opus (independent, fresh-context subagent — complexity:medium)
**Verdict:** Approved, with two comment fixes applied (see below). No code changes required.

## What changed

`internal/pages/ui_smoke_test.go`'s `openPagesTestDB` — a shared test-DB
helper with ~144 call sites — ran the full SQLite migration chain from
scratch on every call. It now clones the same once-built, fully-migrated
template `demo_seed_opt_in_test.go`'s `realDBTemplate` already builds for
`newRealDBDeps` (ut-docs#2191/universal-till#1145), instead of duplicating
a second `sync.Once`/build (both helpers live in the same `pages` package).
Added `TestOpenPagesTestDB_TemplateClonesAreIsolatedAndFullyMigrated`,
mirroring the sibling helper's own isolation test.

No `internal/db` changes, no sqlite driver change, no new SQL outside the
test file itself (`internal/data`/`internal/db` untouched).

## What the review verified independently

- `gofmt -l`, `go build ./...`, `go vet ./internal/pages/...`, and
  `golangci-lint run ./internal/pages/...` all clean.
- `go test ./internal/pages/... -count=1` green (~171-176s, consistent
  with this package's already-documented isolated baseline in
  `.github/workflows/ci.yml`'s own comments, ~204-214s).
- The new isolation test passes (0.02s), and the reviewer manually
  reverted the fix (kept the new test) to confirm the mechanism actually
  matters: the isolation test alone does **not** distinguish the fix from
  the old from-scratch `db.Open` — it is a safety net for the isolation
  property, not a red-first guard for the performance change. Wall-clock
  is the only distinguishing signal. Documented in the test's own comment
  after review.
- `realDBTemplate(t)`'s cached `[]byte` is read-only after its
  `sync.Once`; `internal/pages` has no `t.Parallel()` anywhere
  (`import_stage.go:231` documents this as load-bearing), so the shared
  slice is safe regardless.
- `internal/db/db.go`'s `migrate()` calls `verifyAppliedMigrations`
  unconditionally before applying anything, hard-erroring on a
  name/checksum mismatch (`idempotentRerunVersions` is empty) — confirmed
  against source, not just the sibling helper's comment, that a migration
  file edited after the template was built is still caught on the next
  clone.
- Two stale-comment findings from the review were fixed in this branch:
  `demo_seed_opt_in_test.go`'s header comment (previously pointed at
  `openPagesTestDB` as "the largest remaining lever" — now says it was
  fixed by this card and names the ~74 ad-hoc `db.Open` sites as the
  actual remaining lever) and the "~138" count in `ui_smoke_test.go`'s own
  new comment (actual count today is ~144).

## Acceptance criteria (ut-docs#2219)

- [x] `openPagesTestDB`'s call sites no longer each run a full migration
      chain from scratch; behavior unchanged for every test using it.
- [x] Isolation test added, mirroring `TestNewRealDBDeps_...`.
- [x] `go test ./internal/pages/... -count=1` (no `-race`) stays green.
- [x] `go test ./internal/pages/... -race -count=1` measured and recorded:
      **still FAILs on timeout** — `go test ./internal/pages/... -race
      -count=1 -timeout=900s` ran the full 900.157s budget and hit
      `panic: test timed out after 15m0s`. The test running at the deadline
      (`TestInit_ReconcilesBuiltinLayoutForPreExistingShopType`) had only
      been running 3s — this is aggregate `-race` overhead across the
      package's full test suite exceeding the budget, not a hang/deadlock
      in any single test (same "goroutine dump ≠ real hang, it's fixture
      cost" shape already documented in `ci.yml`'s own comments for
      ut-docs#648/#674/#1992). CI itself does not run `internal/pages`
      under `-race` — its actual gate is `go test -timeout 20m
      ./internal/pages` (plain, no `-race`), which this fix should make
      measurably cheaper without changing behavior, since it removes
      ~144 more from-scratch migration chains from that path.
- [x] No `internal/db` changes.

## Recommendation for the next lever (recorded, not actioned this card)

Per the card's own sequencing ("do `openPagesTestDB` first... then the
~74 ad-hoc sites"), the remaining ~74 ad-hoc `db.Open` call sites across
46 files are the next highest-leverage target for actually getting
`-race` under a reasonable budget — a `-timeout` bump alone would mask
growing aggregate cost rather than fix it. Left as a follow-up; not
adding scope to this card.
