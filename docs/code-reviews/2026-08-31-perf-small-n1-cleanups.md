# Code review — perf: small N+1 cleanups (ut-docs#1323)

- **Date:** 2026-08-31
- **Branch:** `fix/1323-perf-small-n1-cleanups`
- **Reviewer:** independent reviewer, fresh-context Sonnet subagent
  (complexity:easy → Sonnet-reviews-Sonnet, per `scrum-master`'s model
  routing table)
- **Verdict: SAFE TO MERGE. No blocking findings.**

## What shipped

Four independent, low-effort perf fixes bundled per the 2026-08-30
principal-engineer performance audit (`docs/code-reviews/2026-08-30-performance-audit.md`,
section G) — each mechanical, no intended behavior change:

1. `internal/pages/index_page.go` — the sale screen (the product's
   highest-traffic page load) called `Settings.Get` once per active
   payment method to read its `payments.fee.<id>` row. Added
   `SettingsRepo.GetByPrefix` / `settings.Store.GetByPrefix` (one
   prefix-scan query, LIKE-wildcard-escaped) and switched the call site to
   it.
2. `internal/pages/pos_api.go`'s `loadReceiptLegalBlocks` — one
   `PluginRepo.GetPluginVersionAt` call per receipt-template plugin on
   every completed sale. Added a batched
   `PluginRepo.GetPluginVersionsAt(ctx, ids, at) (map[string]string, error)`.
3. `internal/pages/eod_api.go`'s `POST /api/reports/eod/print/{period}`
   handler — loaded up to 100 full `report_archive` rows (including large
   `content_json`) via `ListArchivedReports` and linear-scanned for one
   `(kind, period)` match, despite `report_archive` already carrying a
   `UNIQUE(kind, period)` index. Added `POSRepo.GetArchivedReport` for a
   direct indexed lookup.
4. `internal/data/plugin_repo.go`'s four install-time conflict-check
   methods (`FindPaymentKeyConflicts`, `FindPaymentNameConflicts`,
   `FindPageKeyConflicts`, `FindPageRouteConflicts`) looped one query per
   candidate key/name/route. Rewritten to batch via `IN (...)`.

New tests: `internal/data/plugin_repo_batch_test.go` (behavior +
query-count assertions for all five batched methods/functions), plus two
additions to `internal/data/settings_repo_test.go` for `GetByPrefix`
(including a LIKE-wildcard-escaping regression test).

## Independent review — what was checked

The review ran in an isolated worktree, with no visibility into the
implementation's own reasoning, and traced the diff against the **pre-fix**
code (not just read the after-state in isolation):

- **Ordering under multiple simultaneous conflicts.** `manifest.go`'s
  `validatePaymentEntryKeys`/`validatePageEntryKeys`/
  `validatePageEntryRoutes` only ever report `conflicts[0]` in their error
  message, so a reordering in the batched rewrite would be a silent,
  untested behavior change. Traced every branch of the original per-key
  switch statements in `FindPaymentKeyConflicts`/`FindPaymentNameConflicts`
  against the batched map-based logic by hand; confirmed the rebuild step
  re-iterates the caller's own input slice (not query-result order), so
  `conflicts[0]` is unchanged for every case. Backed by the new test's
  ordered 3-conflict assertions.
- **The two-step fallback logic** (payment_methods row exists and is
  owned by this same plugin → still needs the plugin_entries fallback
  check, since a just-installed sibling plugin's entry may not be synced
  yet) — confirmed every original branch (no row / null-or-empty owner /
  owned-by-other / owned-by-self) is reproduced in the batched version's
  `needEntryCheck` selection.
- **`GetByPrefix`'s LIKE-escaping** — `strings.NewReplacer` applies all
  substitutions in one non-overlapping pass (no re-scan of already-escaped
  output), so `\`/`%`/`_` are correctly single-escaped, not
  double-escaped. Verified by the new escaping test.
- **`GetPluginVersionsAt`'s "most recent per id" logic** — `plugins.id` is
  `TEXT PRIMARY KEY` (migration 001), so there is at most one row per id
  regardless of `updated_at`; the `ORDER BY id, updated_at DESC` +
  first-seen-wins dedup is a defensive no-op against that invariant, not
  a bug, and matches `GetPluginVersionAt`'s own semantics exactly.
- **`GetArchivedReport`** reuses the same `scanArchivedReport` helper and
  identical column list as `ListArchivedReports`, over the existing
  `UNIQUE(kind, period)` index (migration 013) — same output shape by
  construction.
- Ran `go build ./...`, `go vet ./...`, `gofmt -l .` (all clean); the
  targeted new tests (11, all pass); the full `internal/data`,
  `internal/pages`, `internal/plugins`, `internal/settings` packages (all
  pass); `scripts/ci/guard-data-access.sh` (passes — no SQL leaked outside
  `internal/data`/`internal/db`).
- No real client/shop name or secret-shaped literal in the new test data.
  No file I/O in this diff, so the file-write/`os.MkdirAll`/cwd-relative-
  path bug classes don't apply.

### Genuine (positive) behavior note, not a regression

The EOD reprint handler rewrite is not *purely* a performance change: the
old `HasArchivedReport` pre-check (indexed, unaffected by any limit) could
pass, burn a PIN-elevation slot, and then the subsequent
`ListArchivedReports(ctx, 100)` + linear scan could still miss a report
older than the till's most recent 100 archived EOD reports, returning a
false 404. `GetArchivedReport`'s direct indexed lookup fixes this as a
side effect — a strict improvement, called out here rather than left
implicit, per the reviewer's own read.

### Deferred, not blocking

- SQLite's placeholder-count ceiling on one statement is a theoretical
  concern for the `IN (...)` batches, unreachable at today's realistic
  scale (one plugin manifest's entry count) — not worth guarding now.
- `internal/pages/index_page.go`'s `GetByPrefix` error is discarded
  (`feeSettings, _ := ...`) — matches the pre-existing discipline of the
  per-key `Get` calls it replaced, not a new regression.

## Verification performed (implementer)

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./internal/data/...` (full package) | pass, 83.9s |
| `go test ./internal/pages/...` (full package) | pass, 113.9s |
| `go test ./internal/plugins/...` (full package) | pass, 87.8s |
| `go test ./internal/settings/...` | pass |
| `go test ./...` (whole repo) | pass |
| `bash scripts/ci/guard-data-access.sh` | pass |
| `bash scripts/ci/guard-kiosk-engine.sh` | pass |
| `bash scripts/ci/guard-plugin-menu-read.sh` | pass |
| `bash scripts/ci/guard-i18n.sh` | pass |
| `bash scripts/ci/guard-compliance-claims.sh` | pass |
| `bash scripts/ci/guard-htmx-loaded.sh` | pass |

No UI/template changes in this diff, so the UX-guidelines and help-manual
checklist items don't apply.
