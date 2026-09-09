# Plugin repo `updated_at`/`installed_at` timezone bug (ut-docs#1880)

**Card:** universaltill/ut-docs#1880
**Branch:** `fix/1880-plugin-repo-tz-updated-at`
**Complexity:** easy — Dev at Sonnet (inline), Review at a fresh-context
Sonnet subagent, per `scrum-master`'s model-routing table.

## What shipped

`PluginRepo.InstallPlugin` (`internal/data/plugin_repo.go`) bound
`installed_at`/`updated_at` as bare, unformatted `time.Now()` SQL args — a
Go `time.Time` in the process's **Local** location, with a monotonic clock
reading attached. The modernc.org/sqlite driver serializes an unformatted
bound `time.Time` via `t.String()` (e.g.
`"2026-09-09 03:30:51.975620387 -0700 PDT m=+0.075657588"`), which stores a
value with a **space** at byte 11. The read side,
`GetPluginVersionAt`/`GetPluginVersionsAt`, queries with
`at.UTC().Format(time.RFC3339)` (e.g. `"2026-09-09T10:30:51Z"`) — a **`T`**
at the same byte offset. SQLite compares `TEXT` lexicographically, and
since `' ' (0x20) < 'T' (0x54)`, the stored value sorted as "less than"
almost any same-or-later-calendar-day RFC3339 query string regardless of
the actual wall-clock instant, so `updated_at <= ?` was satisfied even for
a query timestamp *before* the real install — not only under a
negative-offset `TZ` as originally reported, but under **any** `TZ`,
confirmed both at `America/Los_Angeles` and default/UTC.

Fix: bind `time.Now().UTC().Format(time.RFC3339)` instead, matching every
other call site already in `plugin_repo.go`.

## Why the originally-cited failing tests didn't actually fail

`TestGetPluginVersionAt` and `TestGetPluginVersionsAt_MatchesSingularSemantics`
both use a `time.Now().Add(-24 * time.Hour)` "before install" query. A
24-hour gap also shifts the RFC3339 **date** component, so the (broken)
lexicographic comparison still gave the correct verdict by accident — the
classic "test that would pass even against broken code" this pipeline has
been bitten by before. Both tests were reproduced as genuinely still
passing, pre-fix, across 10 repeated runs.

## What was added instead

Two new regression tests, one per query path, using a same-calendar-day,
minutes-scale boundary (1 minute before/after install) instead of 24
hours — this boundary does **not** get the accidental date-component cover
and fails for real against the pre-fix code:

- `internal/data/plugin_repo_lifecycle_test.go`:
  `TestGetPluginVersionAt_SameDayBoundary`
- `internal/data/plugin_repo_batch_test.go`:
  `TestGetPluginVersionsAt_SameDayBoundary` (added during review, see
  below — mirrors the singular test for the batched query path)

TDD: both new tests were confirmed failing against the pre-fix code (exact
assertion: `expected no version active one minute before install (same
day), got ok=true err=<nil>`) under both `TZ=America/Los_Angeles` and
`TZ=UTC`, then confirmed passing after the fix.

## Independent review

A fresh-context Sonnet subagent reviewed the diff in an isolated worktree
(`isolation: "worktree"`, per the reviewer skill's mutate-on-shared-disk
guard). **Verdict: PASS — safe to merge.** It independently re-ran the
revert-then-restore TDD verification itself (reverted the one-line fix,
reproduced the exact same failure under both TZs, restored, confirmed
green again) rather than trusting the implementer's claim.

Findings:
- **Nit (fixed):** the batched path (`GetPluginVersionsAt`) had the same
  24-hour-boundary masking gap as the singular path, just untested at the
  narrower boundary. Fixed by adding
  `TestGetPluginVersionsAt_SameDayBoundary` above.
- **Info, not a defect:** `InstallPlugin` calls `time.Now()` twice (once
  per bound arg) — pre-existing, not introduced by this diff. With
  second-precision `RFC3339` formatting this could only make
  `installed_at`/`updated_at` differ by up to 1 second if the two calls
  straddle a wall-clock second boundary. Cosmetic, out of scope for this
  ticket, not fixed.
- Independently re-verified the scoping judgment that
  `settings_repo.go`/`barcode_settings.go`'s `time.Now().UTC()` (not
  `.Format(...)`) bind sites are **not** the same bug class: grepped for
  any range comparison (`<=`/`>=`) against `settings.updated_at` anywhere
  in the tree and found none — every read of that column is an equality
  lookup, `LIKE` prefix scan, or unconditional `SELECT`. Confirmed correct
  and left unfixed, as scoped by the ticket's own acceptance criteria
  ("where the read side normalizes via `.UTC().Format(time.RFC3339)`").
  `pairing_repo.go`'s two call sites were also checked and already format
  consistently on both write and read.

An unrelated worktree-isolation environment anomaly (a background
process reset the review worktree's branch pointer mid-review) was
reported by the reviewer; it worked around it inside its own isolated
worktree without touching the shared feature branch or the main checkout,
and both were confirmed intact afterward. Noted for awareness, not a code
finding.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- Full `go test ./...` (every package, not just `internal/data`) — green.
- `TZ={UTC,Europe/London,America/Los_Angeles,Asia/Tehran,Pacific/Auckland} go test ./internal/data/... -count=1` — all five green (the ticket's full acceptance-criteria TZ set).
- `golangci-lint run ./...` — 0 issues.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh` — pass (no SQL
  moved outside `internal/data`, no user-facing strings touched).
- Every other CI-blocking guard listed in `universal-till/CLAUDE.md`'s
  "Before committing" section — all exit 0 (backend-only change, no
  UI/Android/i18n surface touched, so all were no-ops as expected).
- No visible UI surface touched — the Tester skill's screenshot/visual-
  check requirement does not apply; no manual/help topic or README change
  needed (nothing a shop owner sees or does changed).

## Safe-to-merge verdict

**Yes.** Small, root-caused, TDD-verified fix with an independent review
pass finding no blockers, one nit (fixed), and one accepted-as-out-of-scope
cosmetic note.

## Explicitly deferred

- `InstallPlugin`'s double `time.Now()` call (info-level note above) —
  cosmetic, not filed as a follow-up card; revisit only if it's ever
  observed to matter.
