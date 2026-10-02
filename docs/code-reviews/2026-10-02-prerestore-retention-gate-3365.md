# 2026-10-02 — Pre-restore copies respect the statutory retention floor (ut-docs#3365)

**Lane:** lane:cloud-41b · **Built by:** Opus 5.5 (dev subagent) · **Reviewed by:** Fable (independent subagent, separate worktree)

## What shipped
- `internal/db/backup.go`: `PrunePreRestore` gains a `minRetain time.Duration` parameter. The removal condition `i < keep && age<=maxAge` becomes `(i < keep && age<=maxAge) || age<=minRetain` — the floor overrides both the count rule (`PreRestoreKeep`=3) and the age rule (`PreRestoreMaxAge`=30 days), since the old count rule deleted any file beyond the newest 3 unconditionally, even a young one.
- `internal/data/reset_archive_repo.go`: new exported `ResolveArchiveMinDays(ctx, *sql.DB) (int64, error)`, a thin wrapper over the existing `resolveArchiveMinDays` (ADR-0040's country-retention lookup), so `internal/server` can reuse it without reaching into package-private state.
- `internal/housekeeping/housekeeping.go`: `Run` gains a pass-through `minRetain time.Duration` param — no new imports, so `TestPackageNeverTouchesTheDatabase`'s static-analysis guard (forbidding this package from importing `database/sql`/`internal/data`) stays satisfied; the floor is resolved one layer up and handed down as a plain value.
- `internal/server/server.go`: `runHousekeeping` gains a `db *sql.DB` param, resolves `minDays` via `data.ResolveArchiveMinDays` (falling back to `data.GlobalArchiveMinDays` on a nil DB or a read error — fail safe toward retaining, never toward deleting), clamps it to `maxArchiveRetainDays` (review finding #1, below), converts to a `time.Duration`, passes it down.
- Doc accuracy: `housekeeping.Retention()`'s `KindPreRestore` policy string, and `web/help/{en,de,ar,fa,tr}/backups.md`'s retention bullet — both said "newest 3, none older than 30 days" with no mention of the legal floor, contradicting the very next sentence in the manual ("never removes … any other record you must keep by law"). Translated the one-sentence fix into all 4 non-English locales myself, reusing each file's existing "must keep by law" phrasing.
- New/updated tests: `internal/db/prerestore_prune_test.go` (2 new cases), `internal/housekeeping/housekeeping_test.go` (signature threading), `internal/server/server_test.go` (2 new integration cases against a real migrated DB), `internal/data/resolve_archive_min_days_test.go` (new).

## Review findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `time.Duration(minDays) * 24 * time.Hour` in `runHousekeeping` overflows to negative for `archive_min_days > 106,751` (no upper bound anywhere — only `validateCountrySetting`'s floor of 3650), flipping the floor OFF and deleting a copy it should protect — exactly backwards from the fix's own "fail safe toward retaining" intent | Fixed: clamp to `maxArchiveRetainDays` (`math.MaxInt64 / 24h`, derived not hardcoded) before converting; new regression test `TestRunHousekeeping_ClampsAbsurdArchiveRetentionInsteadOfOverflowing` (`archive_min_days=999999999`), confirmed failing against the unclamped code with the exact predicted symptom (the protected 40-day copy was deleted), passing after the clamp |
| 2 | nit | In production `minRetain` is always ≥3650 days (the floor can't be configured lower) and `PreRestoreMaxAge` is 30 days, so the count/age rule is fully dominated — "newest 3, none older than 30 days" describes dead code in production | Accepted as documented, not a bug; the mechanism still matters for a future lower global floor and is what the existing unit tests exercise directly |
| 3 | nit | `PrunePreRestore` ages from the exact restore timestamp; `reset_archive_repo.go`'s `computeRetainedUntil` ages from the UTC midnight after archival — up to ~24h difference in when something becomes purgeable, at a 10-year horizon | Accepted, pre-existing style, outside this card's scope |
| 4 | nit | mtime fallback (for a pre-restore file whose name doesn't parse as a timestamp) can look older than its real restore time, biasing slightly toward earlier deletion | Accepted, pre-existing behaviour, unchanged by this diff, only reachable for a file not written by `ApplyPendingRestore` |
| 5 | nit | pre-existing over-long doc-comment line in `backup.go` | Accepted, cosmetic, pre-existing |

Reviewer hunted and cleared: import-cycle/layering risk (`internal/db` confirmed not to import `internal/data`; `go list -deps` checked), the nil-`*sql.DB`-panics-rather-than-errors claim (verified with a throwaway test, confirmed, which is why the `if db != nil` guard is load-bearing), edge cases (`age==minRetain`, `minRetain=0`, `keep=0`), the two recurring bug classes (no new file writes / no new cwd-relative paths — both N/A here), and all 5 locale doc edits for natural, accurate phrasing.

## Verified beyond automated tests
- TDD re-verified twice independently: (a) reverting only the `backup.go` removal-condition change made `TestPrunePreRestore_StatutoryFloorOverridesCountAndAge` and `TestRunHousekeeping_KeepsPreRestoreCopiesInsideStatutoryFloor` fail with the real "copy removed" assertion, restoring made them pass; (b) separately neutralising only `runHousekeeping`'s resolve-and-pass-through logic produced the same failure/pass pattern — proving both halves of the wiring are load-bearing, not just one.
- `go list -deps ./internal/db/` and a read of `internal/housekeeping`'s import block, to directly confirm no layering violation was introduced (not just trusted from the diff).
- `TestPackageNeverTouchesTheDatabase` run explicitly, green.
- Full gate after the finding-1 fix: `go build`, `go vet`, `go test ./...` (whole module, no failures), `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-drift.sh`, `guard-help-topics.sh`, `golangci-lint` on the four touched packages — all clean.

## Not verified
No UI/runtime surface exists for this change (a daily background job, no handler/page/plugin boundary) — no screenshot or driven-app check applies. Real hardware / a real multi-year restore-then-wait scenario is infeasible in this environment; covered instead by unit/integration tests against real migrated-DB retention logic, the right layer for background-job retention math.

## Verdict
Safe to merge.
