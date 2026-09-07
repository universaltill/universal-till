# Cache EnabledBarcodeSymbologies in-process (ut-docs#1361)

**Date:** 2026-09-06
**Card:** ut-docs#1361 (split from ut-docs#1317, 2026-08-30 perf audit finding 5)
**Repo/area:** `universal-till`, `internal/data/barcode_settings.go`
**Complexity:** easy

## What shipped

`SettingsRepo.EnabledBarcodeSymbologies` — the shop's enabled barcode
symbology set (ADR-0059 §2), read on every single scan and every
untyped `AddBarcode` call — was re-fetching from SQLite and
re-JSON-parsing on every call, even though the value is manager-toggled
and changes essentially never.

Added an in-process cache, keyed by `*sql.DB` rather than a field on
`*SettingsRepo`: `SettingsRepo` is constructed fresh on nearly every call
site (`data.NewSettingsRepo(db)` in `settings_page.go`,
`catalog/handlers.go`, `import_page.go`, …), so an instance-scoped cache
would never actually be hit twice. Keying by the underlying `*sql.DB`
pointer instead gives every distinct database (one per till process in
production; one per test in the suite) its own cache slot with no
test-only reset hook needed.

Both write paths — `SetEnabledBarcodeSymbologies` (full-list replace) and
`SetBarcodeSymbologyEnabled` (the production toggle behind
`POST /api/settings/barcode-symbology`) — invalidate the cache entry for
their `db` after a successful write/commit.

Read errors (a DB failure or a corrupt/unparseable row) are never cached
— only the two clean, no-error return paths populate the cache — so a
transient failure can't get pinned in place of a retry.

## Independent review

Spawned a fresh-context Sonnet subagent (this is a `complexity:easy`
card — Dev and Review both run at Sonnet, the review's independence
coming from a clean context that never saw the implementation reasoning,
per the `scrum-master` skill's model-routing rules) in an isolated git
worktree. It read the diff and enough of the call graph
(`pos_repo.go`'s scan-path lookup, `catalog_repo.go`'s `AddBarcode`
matcher, `settings_page.go`'s toggle handler) to judge correctness, then
independently ran the full gate itself rather than trusting the
implementer's word.

**Confirmed clean:**
- Invalidation coverage: the only production write path
  (`SetBarcodeSymbologyEnabled`, via the settings-checklist handler) and
  the only other exported writer (`SetEnabledBarcodeSymbologies`) both
  invalidate after a successful write; a repo-wide grep found no other
  write path (`SetMany`, a raw `INSERT`/`UPDATE settings`, `Delete`) that
  touches this key today.
- No aliasing bug: every caller of the returned slice
  (`barcode.Registry.Match`, `computeBarcodeBackfillPlan`,
  `catimport.DeriveNumberBarcode`, the settings-checklist render) only
  reads it — nothing sorts, appends to, or otherwise mutates the shared
  cached backing array in place.
- No error-path caching: both DB-read and JSON-parse failures return the
  uncached `defaults` directly.
- `go test ./internal/data/... -race -run Barcode` — clean, no data race.
- Test quality: the reviewer disabled the cache-read short-circuit and,
  separately, both `invalidateBarcodeSymbologyCache` calls, and confirmed
  the relevant new tests fail with the expected wrong values in each
  case, then confirmed they pass again once restored — the three
  original regression tests are genuine, not tautologies.
- No client/shop name, no secret-shaped literal, no i18n/help-topic/UI
  surface touched (pure backend/data-layer change).

**MAJOR finding, fixed (not just documented as accepted risk):** the
review flagged a logical TOCTOU race — the read path's DB fetch runs
*unlocked* (only the final cache-store takes the write lock), so a
writer's commit + `invalidateBarcodeSymbologyCache` could land while a
reader is still mid-fetch of the pre-write value; that reader would then
store its now-stale result right after the invalidation ran, silently
undoing it. For a setting that "changes essentially never," the wrong
value could stay pinned until the *next* write — plausibly months. Fixed
with a per-`db` generation counter (`barcodeSymbologyGen`): a reader
snapshots the generation before its unlocked fetch and only commits the
fetched value if the generation is still the one it started with;
otherwise a write raced it and the stale result is returned to that one
caller but never written into the cache. Added a deterministic
white-box regression test (`barcode_settings_race_test.go`, in
`package data` so it can drive the unexported generation/cache maps
directly rather than relying on goroutine timing to maybe hit the
window) — confirmed failing (stale value cached) with the guard removed,
passing with it restored.

**Nits, addressed with doc comments (no behavior change needed):**
- The cache/generation maps never evict an entry for a closed `*sql.DB`.
  Harmless in production (one long-lived DB per process) and cheap even
  across a test binary's many short-lived DBs; documented as an accepted
  trade-off rather than adding cleanup machinery.
- `SettingsRepo.Delete`/`SetMany` could, in principle, write this key
  later and bypass invalidation. Not reachable today (verified via
  repo-wide grep), but documented on `BarcodeEnabledSymbologiesKey` as a
  landmine for whoever adds a new write path.

## Verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./...` clean.
- `go test ./...` (full repo) green before the TOCTOU fix; re-ran the
  targeted packages (`internal/data`, `internal/pages`,
  `internal/pages/catalog`, `internal/pages/common`, `internal/ui`) plus
  `-race` on the barcode-settings tests green again after it.
- `golangci-lint run ./...` — 0 issues.
- `bash scripts/ci/guard-data-access.sh` and
  `bash scripts/ci/guard-i18n.sh` — both pass (no surprise, this is a
  pure `internal/data` change with no user-facing string).
- Manually broke and restored each of the four regression tests
  (3 original + the TOCTOU one) to confirm each one genuinely fails
  against the bug it targets, per the `tester` skill's "would this test
  actually fail?" discipline.
- No UI/visible surface touched — the visual-check attestation in the
  `tester` skill doesn't apply; no screenshot needed.

## Safe-to-merge verdict

Safe to merge. Correctness holds for the current call graph (no
aliasing, full invalidation coverage, no error pinning), the TOCTOU race
the review found is fixed (not just accepted), and all four regression
tests are independently confirmed to catch real regressions.

## Explicitly deferred

- Nothing deferred as follow-up work; both review findings that needed a
  code change (the TOCTOU race) or a code comment (the two nits) are
  addressed in this same PR.
