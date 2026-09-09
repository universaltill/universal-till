# item_images UNIQUE(item_id, role) + race fix (ut-docs#1871)

**PR:** universaltill/universal-till (branch `fix/1871-item-images-thumbnail-race`)
**Card:** universaltill/ut-docs#1871
**Complexity:** easy — Dev at Sonnet, Review at Sonnet (fresh context)

## What shipped

`item_images` had no uniqueness constraint on `(item_id, role)`.
`SetItemThumbnail`'s non-atomic UPDATE-then-INSERT let two concurrent calls
for the same item both see the UPDATE affect 0 rows and both fall through to
INSERT, producing two `role='thumbnail'` rows — any reader doing
`SELECT ... LIMIT 1` with no `ORDER BY` (e.g. `ItemThumbnailPath`,
ut-docs#1844) then answered nondeterministically.

- New migration `015_item_images_thumbnail_unique.sql`: dedups any
  pre-existing duplicate rows (keeping the highest-rowid one per
  `(item_id, role)`), then creates `ux_item_images_thumbnail_once`.
- `SetItemThumbnail` now does a single atomic
  `INSERT ... ON CONFLICT(item_id, role) DO UPDATE SET path = excluded.path`
  instead of two separate statements.
- `EnsureDefaultThumbnail` had the identical SELECT-then-INSERT race shape
  (not named in the issue title, but its own "never overwrite" no-op would
  otherwise become an unhandled UNIQUE-constraint error under the same race
  once the index exists) — now a single atomic `INSERT OR IGNORE`.
- `internal/testsupport/sqlite_catalog.go`'s hand-mirrored test schema
  needed the same index added, or `ON CONFLICT` has nothing to match.

## Independent review (Sonnet, fresh context)

Verified empirically, not just by reading: ran `go build`/`go vet`/lint,
ran the targeted and full test suites (including `-race` on `internal/db`
and the thumbnail tests), grepped all of `internal/` for other
`item_images` writers (none — the two repo functions are the only ones,
called only from `internal/pages/catalog/handlers.go`), confirmed the
migration number was free on `origin/main`, and did a genuine TDD
revert-verify: reverted `SetItemThumbnail` to the literal pre-fix
UPDATE-then-INSERT and confirmed the new race test caught it, then
restored.

**Finding, fixed before merge:**

1. **Blocker-class correctness bug**: `CREATE UNIQUE INDEX IF NOT EXISTS`
   only guards against the index itself already existing, not against
   pre-existing data that violates it. Since this migration exists
   *because* the race it closes may already have produced duplicate rows
   on a live till, an unguarded `CREATE UNIQUE INDEX` against such a
   database would fail the migration outright — dropping the till into
   read-only safe mode (ADR-0075) with no self-service repair. Fixed by
   adding a `DELETE ... WHERE rowid NOT IN (SELECT MAX(rowid) ... GROUP BY
   item_id, role)` step ahead of the index, keeping the most recently
   written row per pair. New `TestMigration015DedupsBeforeUniqueIndex`
   runs the real migration file verbatim against a database seeded with
   the exact duplicate shape the pre-fix race produced (proving both that
   the migration no longer fails, and that the *newer* row survives, not
   an arbitrary one); `TestMigration015NoOpOnCleanData` confirms the common
   case (no duplicates — every fresh or already-healthy database) deletes
   nothing.

**Findings noted, not acted on (both genuinely low, and cost more to
"fix" than they're worth):**

- `SetItemThumbnail`'s upsert always generates a fresh `uuid.NewString()`
  even on the update path, where it's discarded (`excluded.id` is never
  read — only `path` is set). A few microseconds of wasted work per call;
  avoiding it would need a pre-check that reintroduces the exact race this
  PR closes, or a second statement variant, for no material benefit.
- The two new concurrency tests (15 rounds × 8 goroutines) have a real but
  fairly low per-run catch probability against a genuinely reverted fix in
  this environment (`TestSetItemThumbnailConcurrentRace`'s own race window
  did not reproduce at all under plain goroutines against this driver, even
  at 40×24 — matches the issue's own "narrow in practice" framing).
  `TestEnsureDefaultThumbnailConcurrentRace` reliably reproduces its half of
  the race. This matches the existing sibling pattern
  (`TestUpdateItemReturningWasActiveConcurrentRace`) already in this
  codebase, not a new problem introduced here — the atomic-statement fix is
  correct by construction (guaranteed by the DB engine), independent of
  whether a goroutine-based test can reliably force the old race.

## Verified

- `gofmt -l .` clean; `go vet ./...` and `golangci-lint run ./...` — 0
  issues.
- `go test $(go list ./... | grep -vE '/internal/plugins$|/internal/plugins/(oauth|marketplace)$')`
  (the exact command `ci.yml`'s `go test` step runs) — all green, plus the
  two excluded plugin dirs run separately, also green.
- `go test ./internal/db/... -race` and
  `go test ./internal/data/... -run Thumbnail -race -v` — all green,
  including both new concurrency tests and both new migration-dedup tests.
- `bash scripts/ci/guard-data-access.sh` and
  `bash scripts/ci/guard-migration-version-collision.sh` — green.
- Not run: the full `internal/data` package suite under `-race` times out
  at the default 600s regardless of this change (pre-existing, separately
  tracked as ut-docs#1366 — confirmed the specific test nearest the
  timeout, `TestPOSRepo_TipAmount_RoundTrips`, passes in 1.46s in
  isolation, so this is cumulative package-wide `-race` overhead, not a
  hang introduced here).
