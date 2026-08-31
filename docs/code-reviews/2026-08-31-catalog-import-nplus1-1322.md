# Code review — perf: catalog import N+1 category/tax-code lookups (ut-docs#1322)

- **Date:** 2026-08-31
- **Branch:** `fix/1322-catalog-import-nplus1`
- **Reviewer:** independent reviewer, fresh-context Opus subagent, isolated
  worktree (`complexity:medium` → Opus review, per `scrum-master`'s model
  routing table)
- **Verdict: SAFE TO MERGE after fixing one blocking finding.** Initial
  review found it NOT safe to merge as originally written; the blocking
  finding was fixed in this same branch and re-verified before merge.

## What shipped

Finding #3 from the 2026-08-30 principal-engineer performance audit
(`docs/code-reviews/2026-08-30-performance-audit.md`, section F). Findings
#1/#2 in the same section (`DumpAdmin`/`ApplyAdmin` multi-till sync
protocol changes) touch offline-first sync guarantees directly and were
judged to need their own careful design/review pass rather than being
bundled into this contained fix — split off as ut-docs#1368 and
ut-docs#1369 during BA scoping (this card, ut-docs#1322, was retitled and
scoped down to finding #3 only).

1. **CSV/Excel catalog import did N+1 category/tax-code lookups per row
   instead of per distinct value** (`internal/pages/import_page.go`'s
   commit row loop calling `CatalogRepo.EnsureCategoryUnder` and
   `CatalogRepo.FindOrCreateTaxCode`). A 2,000-3,000 row import across
   ~30 categories issued thousands of redundant lookup queries instead of
   ~30-60, directly slowing the onboarding flow a shop owner is actively
   watching. Fixed with two local, per-commit-request in-memory caches
   (`categoryCache`, `taxCodeCache`), populated on first miss per distinct
   (name, parent-id) / (rate, takeaway-rate) value. No repository-layer or
   SQL change — the caches sit entirely in the caller, so the repository
   pattern (`CLAUDE.md`) is untouched.
2. New tests:
   - `TestImport_CategoryAndTaxCodeLookupsAreCachedPerRun`
     (`internal/pages/import_page_querycount_test.go`) — opens a second,
     counting connection to the same on-disk DB (same technique as
     `internal/data`'s `TestSalesForExport_ConstantQueryCount`) and
     asserts the `categories`/`tax_codes` SELECT count for a 40-row/8-
     distinct-value import stays near the distinct-value count, not the
     row count. Vacuity-checked: reverting only the production fix (test
     kept) fails with `SELECT count 80 … want roughly <=8` — exactly 2
     lookups/row, i.e. the pre-fix shape.
   - `TestImport_CategoryCacheFoldsOnlyASCIICase`
     (`internal/pages/import_page_test.go`) — the regression test for the
     blocking finding below.

## Independent review — what was checked

Full findings and verification transcript from the review subagent (ran
in an isolated worktree, built/vet/gofmt/guards + the full `internal/pages`
and `internal/data` suites, `-race` on the touched tests, plus a
throwaway probe to demonstrate the blocking finding empirically):

### Blocker (fixed) — category cache key over-folded case vs. the DB's own NOCASE

The cache key originally used Go's `strings.ToLower` (Unicode-aware).
`EnsureCategoryUnder`'s own DB lookup uses SQLite `COLLATE NOCASE`, which
folds **ASCII only** — `Ä` and `ä` are distinct to the DB. The reviewer
demonstrated: seed a category `"Getränke"`, then in one later import
request feed row 1 `"GETRÄNKE"` (genuinely a new category to the DB, since
NOCASE doesn't fold Ä) followed by row 2 with the exact original spelling
`"Getränke"` — with the Unicode-folding key, row 2's cache lookup
incorrectly hit row 1's freshly-created category instead of the real
pre-existing one, because `strings.ToLower` collapsed both spellings onto
one key. Directly in scope for the Germany pilot's own café vocabulary
(Getränke, Käse, Bäckerei, Süßwaren) and the tr/fa/ar/he locales this
product ships.

**Fix:** added `asciiFoldLower`, folding only `'A'-'Z'`, so the cache key
agrees with NOCASE exactly on which values the DB considers equal. Added
`TestImport_CategoryCacheFoldsOnlyASCIICase`, which reproduces the
reviewer's exact scenario (seed `Getränke`, then one request with
`GETRÄNKE` + `Getränke` rows, assert the exact-spelling row lands in the
original category). Verified red→green: with the fold reverted to
`strings.ToLower`, the new test fails with the exact wrong-category id;
restoring the ASCII-only fold passes it.

The mismatch is one-directional — everything the DB's NOCASE already
considers equal (ASCII case, leading/trailing whitespace, both trimmed by
`EnsureCategoryUnder` and the cache key alike), the cache also considers
equal, so there is no missed-cache-hit / duplicate-row regression in the
other direction.

### Non-blocking (fixed) — counting test connection missed two production pragmas

The new query-count test's counting connection (`import_page_querycount_test.go`)
was copied from `internal/data`'s read-only `export_repo_querycount_test.go`
pattern, which only sets `busy_timeout`/`journal_mode(WAL)`. This test
drives the *write* path (item inserts, FK columns, a `BeginTx` per row),
so it needs the same pragma string `internal/db.Open` actually uses in
production (`foreign_keys(1)` + `_txlock=immediate` in addition) — measured
`PRAGMA foreign_keys` was `0` on the counting connection vs. `1` in
production, meaning the test could have passed even against writes an FK
constraint should reject. Fixed by reusing `internal/db.Open`'s exact DSN
pragma string.

### Non-blocking (accepted as-is, simplified) — dead `created` field

The tax-code cache originally stored a struct with an unused `created`
bool (a cache hit always correctly returns `false` — see below). Not a
bug, but simplified to a plain `map[string]string` to remove the
dead field per the reviewer's suggestion, since the code was already
being touched.

### Non-blocking (informational, no action) — theoretical `\x00`-separator collision

The category cache key's separator collision would need a NUL byte inside
a CSV category name paired with a known category UUID — `parentID` is
never attacker/CSV-controlled, so not reachable in practice. The tax-code
key has no such ambiguity (`strconv.Itoa` never emits a value that could
be confused across the boundary).

### Explicitly verified and found correct

- **Tax-code cache key `nil` vs. `&0`** — distinct keys
  (`"190\x00"` vs `"19\x000"`), matching `FindOrCreateTaxCode`'s own
  NULL-safe `IS ?` semantics. Empirically confirmed: a file with a blank
  takeaway cell and an explicit `0` takeaway cell at the same dine-in rate
  produced two separate tax codes, and repeats of each reused the correct
  one.
- **`taxCodesCreated` counter stays exact** — its only consumer is the
  audit-log payload (no summary-banner math depends on it); a cache hit
  correctly never re-reports `created`, reproducing pre-fix behavior for
  a repeated value.
- **No cache poisoning on error** — both cached closures return before
  writing the map on a repo error, so a failed lookup leaves no stale
  entry; the existing per-row failure path (`Status`/`Failed`/`failed++`/
  `continue`) is unchanged and still reached by every existing failure
  test.
- **Cross-request idempotency preserved** — both cache maps are declared
  inside the `POST /api/import` handler closure's `if commit {` block,
  not at `registerImport` scope, so a fresh HTTP request gets fresh maps
  and re-queries the DB; re-running the same import still dedups against
  real rows (`TestImport_CommitCreatesCatalog`'s existing re-commit
  assertion still passes unchanged).
- **Concurrency** — the row loop is single-goroutine (no `go func`/
  `errgroup`/`sync.` in the file), so plain maps are safe; `-race` clean
  on the touched tests.
- **Repository pattern / i18n / offline-first** — no SQL added outside
  `internal/data`; no new user-facing strings; the change only adds an
  in-process map on the import *commit* path (not checkout), no new
  network dependency. `guard-data-access.sh` and `guard-i18n.sh` both pass.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` / `go vet ./...` | pass / pass |
| `guard-data-access.sh` / `guard-i18n.sh` | pass / pass |
| `go test ./internal/pages/... ./internal/data/...` | pass |
| `-race` on the two new + related existing tests | pass |
| TDD claims independently re-verified (both new tests) | red pre-fix, green post-fix, confirmed by the reviewer AND re-confirmed after the B1 fix landed |

No UI-visible change (import behaviour, error paths, and idempotency are
unchanged from an operator's perspective — only the query count behind
the commit endpoint changed), so no manual/help-topic update is needed
per `CLAUDE.md`'s "manual ships with the feature" rule.

## Deferred (own cards, not this PR)

- ut-docs#1368 — `DumpAdmin` change-marker (multi-till sync protocol).
- ut-docs#1369 — `ApplyAdmin` batching/diff (multi-till sync protocol),
  sequenced after ut-docs#1368.
