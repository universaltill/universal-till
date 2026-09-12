# Code review: close leaked `*sql.DB` handles in 5 test-DB helpers (ut-docs#2180)

## What shipped

`internal/testsupport.NewCatalogTestDB` (26+ call sites, the card's
original finding) and four `internal/data`-local helpers with the
identical shape — `newAuditTestDB`, `newFiscalChipTestDB`,
`newPluginRepoTestDB`, `newTranslationTestDB` (found via this card's own
"grep for other instances of the same defect shape" acceptance
criterion) — each opened an in-memory sqlite `*sql.DB` and returned it
with no `t.Cleanup`/`.Close()` call inside the helper itself. Every
caller had to remember its own `defer db.Close()`; a real, current subset
didn't: 24 of `NewCatalogTestDB`'s 116 call sites, and **all** call sites
of the other four helpers, leaked the handle (and its background
`database/sql.(*DB).connectionOpener` goroutine) for the life of the test
binary.

Fix: each of the 5 helpers now calls `t.Cleanup(func() { db.Close() })`
right after a successful `sql.Open`, so the helper closes itself exactly
once per test regardless of caller discipline. `database/sql`'s
`(*DB).Close()` is idempotent by design (checks `db.closed` under a
mutex, returns `nil` on a second call — stdlib comment: "Make DB.Close
idempotent"), so this is safe alongside the ~92 call sites that already
had their own `defer db.Close()`.

No production/shipped code touched — this is entirely test
infrastructure (`*_test.go` files plus one `internal/testsupport` helper
used only by tests).

## Verification beyond automated tests

- **TDD, test-first**: for each of the 5 helpers, wrote a regression test
  (`TestXxx_ClosesOnCleanup`) that opens the DB inside a `t.Run` subtest,
  asserts it's usable there, then asserts `Ping()` fails once the subtest
  finishes — confirmed each one fails against the pre-fix code with
  `"... but Ping still succeeded"`, then passes after the one-line fix.
- **`internal/data` `-race` timing re-measured** (this card's AC2, given
  #1366's speculation that this leak might explain its slow `-race`
  runtime): `go test ./internal/data/... -race -timeout 30m` → `1631.259s`
  (real 28m5s), all green, no races. This is **not** a meaningful
  improvement over the pre-existing baselines (#1366's original
  1290s/1306s measurements; universal-till#1135's very recent 1421.642s
  worst-case, merged the same day) — if anything it's higher, which is
  consistent with normal machine/load variance across runs rather than a
  regression caused by this fix. **Finding**: closing these leaked
  handles does not meaningfully speed up the `-race` suite, so #1366's
  "likely candidate" of leaked-handle buildup is not the dominant cost —
  the per-test schema/migration cost or `-race`'s own per-goroutine
  instrumentation overhead (#1366's other listed candidate) remains the
  better explanation. Noted as a comment on #1366 (already closed via
  universal-till#1135's documented `-timeout` override, so not reopened —
  this is additive information, not a reason to revisit that PR's
  resolution).

## Independent review

Reviewed by a fresh-context Sonnet subagent (per this card's
`complexity:easy` label — cheap model wrote it, an independent instance
of the same tier reviews it) in an isolated git worktree branched from
this PR's own commit. Findings:

- **Verdict: SAFE TO MERGE. No blocking issues.**
- Personally re-ran the revert→run→restore TDD check on 2 of the 5 fixes
  (`newFiscalChipTestDB`, `NewCatalogTestDB`): removed the `t.Cleanup`
  line, confirmed the matching regression test failed with the exact
  expected message, restored, confirmed it passed again.
- Confirmed `t.Cleanup` is placed after the `sql.Open` error check in all
  5 helpers (never registers a close on a nil/half-open DB).
- Checked all call sites of the 5 helpers across
  `internal/data`/`internal/pages`/`internal/pos`/`internal/cloudsync`/
  `internal/ui` for a "closed too early, reused later in the same test"
  hazard (e.g. via `t.Parallel()` or a goroutine outliving the helper's
  own test) — none found.
- Confirmed the `Ping()`-after-cleanup regression-test pattern is
  deterministic, not flaky: `t.Cleanup` on a subtest's `*testing.T` runs
  synchronously before the (non-parallel) `t.Run` call returns.
- No secrets, credentials, or real client/shop names in the diff.
- Confirmed the fix's scope (patch the 5 helper definitions, not every
  individual call site) is correct and minimal — it covers every
  existing and future caller.
- **Non-blocking FYI, filed as a follow-up rather than fixed here**: a
  broader grep found ~40 other `_test.go` files across the repo with
  their own local, unaudited `sql.Open(":memory:")` helpers of
  potentially the same shape (3 spot-checked: `internal/auth`,
  `internal/pos/register_identity_test.go`, `internal/ui/buttons_test.go`
  — none had a nearby `Cleanup`/`Close`, though `internal/ui/buttons_test.go`
  was separately confirmed safe in this card's own scoping pass: all 6 of
  its call sites already `defer db.Close()` themselves). Out of this
  card's scope (which was specifically the 5 named helpers) — filed as
  universaltill/ut-docs#2202 for a full repo-wide sweep.

## Gate (build/vet/lint/tests/guards) — full log

- `gofmt -l` — clean on all touched files.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `golangci-lint run ./...` (v2.5.0, matches CI's pin) — 0 issues.
- `go test ./...` (full repo) — all 59 packages `ok`, no failures.
- `go test ./internal/data/... -race -timeout 30m` — green, no races
  (1631.259s; see timing note above).
- `bash scripts/ci/guard-data-access.sh` — passes (no SQL outside
  `internal/data`/`internal/db`; this diff adds none).
- `bash scripts/ci/guard-i18n.sh` — passes (no user-facing strings in
  this diff).

## Scope notes

- No UI surface touched → no UX pass, no `web/help/` manual update, no
  screenshot regeneration needed.
- No money, i18n, offline-first, or plugin-signing concerns — pure Go
  test infrastructure.
- No `README.md` claim made stale by this change.

## Deferred / follow-up

- universaltill/ut-docs#2202 — repo-wide sweep of the remaining ~40
  unaudited `sql.Open(":memory:")` test-DB helpers for the same
  leak shape (filed by this cycle, not built here).

**Safe to merge.**
