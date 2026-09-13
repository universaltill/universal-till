# 2026-09-13 — Repo-wide sql.Open("sqlite") leak sweep: one genuine miss (ut-docs#2202)

## What shipped

Follow-up to ut-docs#2180's independent review, which found a broader,
unaudited surface: a repo-wide grep for `sql.Open("sqlite"` across
`*_test.go` files turns up ~46 files with their own local helper (or
inline call) of a similar shape, only 5 of which #2180 actually fixed
(`testsupport.NewCatalogTestDB` plus 4 `internal/data`-local siblings).

This card audited the full remaining surface. For every file matching
`sql.Open("sqlite"`, traced whether the returned `*sql.DB` is closed —
either within its own function (`t.Cleanup`/`defer .Close()`), or, for a
shared helper, at **every** call site of that helper, recursively through
multi-level wrapper chains (e.g. `internal/ui`'s `setupTestDB` →
`setupFullTestDB` → `newButtonsHTTP`/`seedEmbeddedFixture`/etc., ~25 call
sites in total).

**Result: exactly one genuine leak**, `TestSettingsRepo_SetAndGet`
(`internal/data/settings_repo_test.go`) — it opens
`sql.Open("sqlite", ":memory:")` directly instead of going through the
file's own `newSettingsTestDB` helper (used by every other test in the
file), so it never picked up that helper's #2180 `t.Cleanup` fix. Fixed
with the identical one-line pattern: `t.Cleanup(func() { db.Close() })`
right after the open, before the schema `CREATE TABLE`.

Every other helper on the ~46-file surface is already safe today, either
via its own self-cleanup or via universal caller discipline — matching
the pattern the parent issue already called out for
`internal/ui/buttons_test.go`'s own `setupTestDB` (fragile — depends on
every caller remembering to close — but not currently leaking). Per the
issue's own explicit scope ("no need to 'harden' something that isn't
actually leaking today"), no other code change is made.

## Independent review

Reviewed by a fresh-context Sonnet subagent (complexity:easy → Sonnet
builds, fresh-context Sonnet reviews), in an isolated git worktree.

**Findings:** none. The reviewer independently re-ran the repo-wide grep
(confirmed the same 46-file count), manually traced 13 of those files —
deliberately including the `internal/ui` multi-level wrapper chain and
the two other largest fan-out helpers (`setupSaleDB`, `testShiftDB`,
~25 call sites each) — and confirmed every one closes what it opens,
including two call sites that manually close the db mid-test to force an
error path deliberately (not a leak). No additional leak found. The
one-line fix was confirmed minimal (an entire-branch diff stat of 1 file
/ 1 insertion) and correctly matches the file's own established pattern.
`t.Cleanup` runs after the test body, so it cannot affect any assertion
ordering — confirmed by the reviewer's own green `-race` run.

**Deferred, not fixed:** nothing — the audit itself, with no other leak
found, is the complete scope of this card.

## Verified (beyond automated tests)

- `go build ./...`, `go vet ./...` — clean (run independently by both
  Dev/Tester and the reviewer).
- `go test ./internal/data/... -run TestSettingsRepo -v -race` — all 12
  subtests pass, including the fixed test (run independently by both).
- `go test ./...` (whole repo) — clean, zero failures (dev/tester run).
- `bash scripts/ci/guard-data-access.sh` — clean (run independently by
  both): `✓ data-access guard: no inline SQL outside internal/data /
  internal/db`.
- `bash scripts/ci/guard-i18n.sh` — clean (no user-facing strings
  touched; test-only change).
- No real client/shop name, no literal secret-shaped value anywhere in
  the diff (trivially true for a one-line `t.Cleanup` addition).
- No UI surface touched — `reference/ux-guidelines.md` checklist and the
  help-manual/screenshot rule are both N/A.
- Neither of the two standing recurring bug classes (missing
  `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)`) applies
  — the change is a test-only, in-memory (`:memory:`) sqlite cleanup, no
  disk writes.

**Verdict: safe to merge.** No blocking findings, no deferred follow-up
work — the sweep itself, with one genuine leak found and fixed, is the
complete resolution of ut-docs#2202.
