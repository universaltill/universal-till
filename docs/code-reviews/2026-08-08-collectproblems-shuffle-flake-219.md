# TestCollectProblems_IncludesFailedPluginInstalls flakes under -shuffle=on (ut-docs#219)

## What shipped

`internal/pages/cloudsync_wire_test.go`, `TestCollectProblems_IncludesFailedPluginInstalls`:
added a single `logging.ResetRecent()` call (plus an explanatory comment)
right after test-dep setup and before seeding the failed-install DB record.
No production code touched.

## Root cause

`collectProblems` (`internal/pages/cloudsync_wire.go:201-243`) builds the
heartbeat's problems digest in two passes, sharing one `maxProblems = 20`
cap: first it drains `logging.Recent()` (a process-global 50-entry ring
shared by the *entire* `go test` binary), then — only while slots remain —
it appends DB-backed failed-install records. Under `-shuffle=on`, other
tests that happen to run earlier in the same process can log up to 20
warn/error lines before this test even starts, filling the cap before the
DB-backed loop is ever reached — silently starving the exact entry this
test asserts on. Not a production bug: the 20-entry cap and the shared
`logging.Recent()` ring are both intentional in a real running till; this
is purely a test-isolation gap, confirmed pre-existing and unrelated to
whatever change happened to surface it (first flagged during ut-docs#177's
review).

## Fix

Reuses an **existing, already-shipped** isolation primitive rather than
inventing a new one: `logging.ResetRecent()` (`internal/logging/logging.go`,
built for ut-docs#404) clears the shared ring — test-only, no production
caller. `internal/pages/stock_ownership_test.go` already calls it
immediately before each of its own scenarios (lines 173, 201, 271) for the
identical reason, documented in that file's own comment block (lines
27-38). This diff applies the same pattern to the one test named in the
issue.

## Independent review (Sonnet, fresh context — complexity:easy per routing)

**Verdict: SAFE TO MERGE.** No findings (blocker, minor, or informational)
against the diff itself.

Re-derived the root cause independently by reading `collectProblems` and
`logging.go` in full (not from this brief), confirmed via
`grep -rn "ResetRecent"` that the only callers repo-wide are
`stock_ownership_test.go` and this new call, and confirmed neither
`cloudsync_wire_test.go` nor `stock_ownership_test.go` uses `t.Parallel()`
(so `-shuffle=on` only changes ordering, not concurrency, as the fix
assumes). Checked that no other test depends on this specific test leaving
residual ring state behind (the two sibling `collectProblems` tests assert
by content marker, not ring length).

**Re-verified the TDD claim directly** rather than taking it on trust, in
an isolated worktree (`isolation: "worktree"`, per this pipeline's
ut-docs#386 mitigation — never on the orchestrator's shared checkout):
ran `go test ./internal/pages/ -run
'TestCollectProblems_IncludesFailedPluginInstalls|TestTenderHandler|TestApplyJournal|TestSyncChip|TestPlugin'
-shuffle=on -count=1`, 15 iterations each way.

- **Pre-fix** (`ResetRecent()` reverted): **10 failures / 15 runs**, every
  failure the exact predicted symptom —
  `cloudsync_wire_test.go:346: expected the failed install to appear in
  collectProblems output`.
- **Post-fix** (byte-identical to this branch's file, diffed to confirm):
  **15/15 pass**.

Also ran the full `internal/pages` package (no shuffle) with the fix
applied: green (`69.453s`), plus `catalog` and `common` sub-packages.
`go build ./...`, `go vet ./...`, `gofmt -l` on the changed file — all
clean.

One informational note (not a defect, not introduced by this diff):
`ResetRecent()` only guarantees the ring is empty *at the moment it's
called* — a background goroutine from an unrelated test logging ≥20 lines
in the narrow window between the reset and the DB read could theoretically
still exhaust the cap. This is an already-accepted residual property of
the identical pattern `stock_ownership_test.go` ships today, not something
this diff needs to solve, and the 15/15 clean run is strong evidence it
isn't a practical problem in this package.

## Verified beyond automated tests

- Diff scope confirmed to be exactly one file, additive-only, inside one
  test function — no production code, migrations, templates, or locale
  files touched. Repository-pattern / money / i18n / offline-first /
  plugin-signing rules are genuinely not applicable.
- No SQL text, no file I/O (so neither of the two recurring bug classes —
  missing `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)` —
  apply), no secret-shaped literal, no real client/shop name in the diff.
- Test-only change with no shop-owner-visible behavior, so no
  `web/help/` manual update applies.

## Additional gate evidence (this cycle, orchestrator-run, outside the review worktree)

- `go test ./... -race`: full repo green (all packages `ok`).
- `go test ./internal/pages/ -shuffle=on -count=1`, 15 runs against the
  same targeted `-run` set: 15/15 pass.
- `go test ./internal/pages/... -shuffle=on -count=3 -timeout=25m` (whole
  package, 3 shuffled passes): green, `143.322s`.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-docs-shots.sh`: green
  (unaffected — test-only change, outside both guards' hashed/scanned surfaces).

## Safe-to-merge verdict

Yes. No deferred items.
