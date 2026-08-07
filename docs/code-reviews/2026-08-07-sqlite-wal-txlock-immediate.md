# SQLite WAL + DSN-wide `_txlock=immediate` closes a lock-contention gap (ut-docs#311)

## What shipped

`internal/db/db.go`'s `Open()` DSN opened SQLite in the default deferred-
BEGIN, rollback-journal mode with only `_pragma=busy_timeout(5000)`. A
transaction that reads first (SHARED lock) and then writes — the
check-then-insert shape most write paths in this repo use — failed
**instantly** with `SQLITE_BUSY` (~20µs, empirically measured, see below)
if another connection already held a RESERVED write lock, because
SQLite's deadlock-avoidance logic skips the busy handler specifically on
a SHARED→RESERVED lock promotion. The configured 5s `busy_timeout` never
applied to that case. Reachable today via any two concurrent write paths
(e.g. cloud-sync's `ApplyAdmin` holding RESERVED while an ordinary write
comes in), not hypothetical — found and measured during ut-docs#304's
independent review.

Fix, added to the DSN:

- **`_txlock=immediate`** — the actual fix. Makes every `database/sql`
  `Begin`/`BeginTx` on the pool run `BEGIN IMMEDIATE` instead of a plain
  deferred `BEGIN`, so the write lock is acquired **at BEGIN** — a code
  path SQLite's busy handler *does* service — rather than at the first
  write statement after a read. A concurrent writer now waits up to
  `busy_timeout` instead of failing instantly.
- **`journal_mode(WAL)`** — a separate, complementary win added
  alongside it: WAL's MVCC means readers no longer block on the writer at
  all. (Confirmed NOT the fix for the instant-`SQLITE_BUSY` bug itself —
  the same read-then-write promotion fails as `SQLITE_BUSY_SNAPSHOT` in
  WAL mode too, which the busy handler equally doesn't retry. The review
  caught the db.go comment slightly implying both params were load-bearing
  for the fix; reworded to attribute the fix to `_txlock=immediate` alone
  and describe WAL as the separate win it is.)

Given the DSN now guarantees every `BeginTx` is IMMEDIATE, `AddBarcode`
(`internal/data/catalog_repo.go`, added for ut-docs#304) no longer needs
its own dedicated-connection + raw `BEGIN IMMEDIATE`/`ROLLBACK`/`COMMIT`
text and the cancellation-safety dance that raw approach required
(`context.WithoutCancel`, a manual `driver.ErrBadConn` discard on rollback
failure) — simplified back to the repo's standard
`r.db.BeginTx(ctx, nil)` / `defer tx.Rollback()` / `tx.Commit()` idiom
(matching `related_items_repo.go`'s `Rebuild`). `addBarcodeInTx` and
`ensureBarcodeAvailable` now take `*sql.Tx` instead of `*sql.Conn`.

Comment-only, no behavior change: `settings_repo.go`'s `GetOrCreate` doc
comment explained an INSERT-before-SELECT ordering trick that was
load-bearing only under the old deferred-by-default DSN; reworded as a
historical note now that `_txlock=immediate` makes every `BeginTx`
IMMEDIATE regardless of statement order.

## Tests

- `TestOpenUsesWALJournalMode` (`internal/db/open_test.go`): opens a real
  file-backed DB via `db.Open`, asserts `PRAGMA journal_mode` = `wal`.
- `TestConcurrentWriterWaitsInsteadOfInstantBusy` (`internal/db/open_test.go`,
  the actual regression test for the bug): connection A holds an open
  write transaction for 200ms; connection B's `BeginTx` + read + write is
  timed. Asserts both that it takes ≥100ms (genuinely waited, not an
  instant failure) **and** succeeds once A commits — fast-and-error is the
  bug, fast-and-no-error would mean the lock isn't real, slow-and-error
  would mean `busy_timeout` expired.
- `TestAddBarcodeConcurrentRace` (`internal/data/catalog_repo_concurrency_test.go`,
  pre-existing, ut-docs#304's own regression guard): unchanged, re-run
  repeatedly through the refactor to confirm the atomicity guarantee still
  holds under the new plain-`sql.Tx` code path.

**Confirmed test-first, independently, twice** — once by Dev, re-verified
again by the Reviewer subagent: `git stash push -- internal/db/db.go`,
both new tests fail for the right reason (`journal_mode = "delete", want
wal`; concurrent write fails on the **error** branch —
`database is locked (5) (SQLITE_BUSY)` — not the timing branch), restore
the fix, both pass. 5/5 repeat runs, no flake.

## Independent review (Opus subagent, complexity:hard)

Verdict: **safe to merge as-is.** No blocking issues.

The reviewer went further than reading the diff — it independently
verified the locking claim by writing a throwaway timing probe (since
deleted) and measuring both branches directly:

| phase | pre-fix | post-fix |
|---|---|---|
| BEGIN | 190µs | 332ms (waited out a 300ms hold) |
| SELECT | 1.47ms | 107µs |
| INSERT | 16.6µs → `SQLITE_BUSY` | 33µs, no error |

This confirms both the ~20µs instant-failure figure and that the busy
handler genuinely runs at `BEGIN IMMEDIATE` — the code comments' claims
are accurate, not asserted on faith. It also traced Go 1.25's
`database/sql` source directly (`awaitDone`/`keepConnOnRollback` in
`sql.go`, plus the driver's `Rollback` implementation) to confirm the
deleted cancellation-safety dance is fully subsumed by stdlib behavior:
modernc's driver implements neither `SessionResetter` nor
`Validator`, so a cancelled context still discards (never pools) the
connection, and the driver's `Rollback` runs on `context.Background()`
internally, so it still reaches SQLite even on a cancelled request
context. Confirmed **no connection leak** — the refactor is a genuine
simplification, not a regression.

One **behavior change** worth recording (not a defect, arguably a
correctness improvement): the old raw-SQL code committed even on a
cancelled request context (`context.WithoutCancel`); the new plain
`sql.Tx.Commit()` checks `ctx.Done()` first and rolls back instead. A
client disconnect mid-`AddBarcode` now aborts the write rather than
completing it. Accepted as-is — not committing abandoned work on a
dropped connection is the more correct behavior — but noted here since
it's an observable change nobody explicitly asked for.

Findings, both minor, neither in this diff's own files:

1. **stale comment, initially fixed then reverted — filed as a follow-up
   instead.** `internal/pages/import_page.go` still describes `AddBarcode`
   as owning "its own #304 BEGIN IMMEDIATE transaction on a separate
   connection" without mentioning ut-docs#311 made that DSN-wide rather
   than a dedicated-connection special case. First folded the reword into
   this branch, but that touches a non-test `internal/pages/**.go` file,
   which trips `guard-docs-shots.sh`'s surface-hash check regardless of
   whether the change is visual (CI caught this live — the local gate run
   before commit hadn't included this guard, a gap worth remembering for
   next time). A comment-only, zero-behavior-change edit doesn't justify a
   full `make docs-shots` screenshot regen across 11 topics × 4 locales,
   so reverted the `import_page.go` hunk out of this diff and filed it as
   its own tiny follow-up card instead (ut-docs#377) rather than silently
   dropping it.
2. **informational only, no action** — `internal/testsupport/sqlite_catalog.go`
   opens `:memory:` directly (bypassing `db.Open`), so those tests don't
   pick up `_txlock`/WAL. Pre-existing, harmless (`:memory:` gives each
   pooled connection its own isolated database regardless of DSN
   locking params) — the new regression tests correctly use a real
   file-backed `db.Open` for exactly this reason.

Also independently re-checked, not just taken on the design note's word:

- **`_txlock=immediate` is genuinely DSN-wide**: confirmed in
  `modernc.org/sqlite@v1.29.10/sqlite.go` — parsed once per connection at
  open (`applyQueryParams`), applies to every `Begin`/`BeginTx` on the
  pool, not just this diff's call sites.
- **No long-lived read-only transaction exists anywhere** that would now
  unnecessarily hold a write-intent lock: enumerated all 17 non-test
  `Begin`/`BeginTx` call sites across `internal/`, all are writes, all
  pass `nil` `TxOptions`, zero uses of `sql.TxOptions{ReadOnly: true}`
  anywhere in the repo.
- **No self-deadlock risk** from `AddBarcode` now running under the same
  DSN guarantee as its caller: `import_page.go`'s import-row transaction
  commits *before* calling `AddBarcode`, so there's no nested-transaction
  path.
- **No offline-first regression**: no transaction spans network I/O
  (`SyncAdminRepo.ApplyAdmin` operates on an already-fetched in-memory
  bundle); worst case under contention a writer now waits up to
  `busy_timeout` instead of failing in ~20µs — strictly better for a real
  sale than an instant, uncaught failure.
- **WAL is not dead weight and carries no filesystem risk for this
  product**: backup (`Snapshot`) uses `VACUUM INTO`, which is WAL-safe and
  consistent, not a raw file copy; `ApplyPendingRestore` already cleans up
  `-wal`/`-shm` sidecars; nothing in the repo copies the live `.db` file
  bare. WAL needs local-disk shared memory and doesn't work over
  NFS/SMB — checked against this product's actual architecture (ADR-0011:
  each till keeps its own local SQLite, syncs over LAN HTTP; the only
  network-filesystem-adjacent tier is the cloud side's Postgres, unrelated
  to the till's SQLite) and confirmed there's no shared-DB-over-network
  scenario in this product's design.
- **No file I/O in this diff at all** — confirmed by reading, not
  assumed, so the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data`)
  don't apply here.
- **No ADR needed** — read the full ADR index and grepped all of them for
  `wal|journal_mode|busy_timeout|txlock|sqlite`; nothing constrains SQLite
  journal mode or transaction-locking behavior, ADR-0003 isn't
  contradicted. Confirmed independently, not just taken on the design
  note's own call.
- No user-facing strings, no manual/`web/help/` surface touched (backend
  only) — reviewer agrees no manual update is needed.
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `gofmt -l` on every changed file — no output.
- `bash scripts/ci/guard-data-access.sh` — passes (all raw SQL stayed in
  `internal/data`/`internal/db`).
- `bash scripts/ci/guard-i18n.sh` — passes (841 keys, no drift; this diff
  adds no user-facing strings).
- `bash scripts/ci/guard-help-topics.sh` — passes (no manual-surface
  change).
- `bash scripts/ci/guard-docs-shots.sh` — passes on the final diff (11
  routed topics × 4 locales, surface hash fresh). Caught the reverted
  `import_page.go` comment tweak live in CI (see finding #1 above) before
  this was added to the local pre-commit gate.
- Full Playwright e2e suite (`e2e/`, both `default` and `auth` projects,
  67 specs): 66 passed. One pre-existing, unrelated failure —
  `catalog-image-to-till.spec.ts`'s thumbnail `naturalWidth`/`complete`
  assertion — confirmed by stashing this entire diff and re-running: it
  fails identically on a clean `main` tree, an image-load-timing artifact
  of this sandbox, nothing to do with SQLite locking. (Required a local,
  uncommitted `playwright.config.ts` tweak to point at this environment's
  pre-installed Chromium binary rather than the browser revision the
  pinned `@playwright/test` version expects — reverted before committing,
  not part of this diff; not a product concern, purely a sandbox
  chromium-version mismatch.)
- No form/dialog/page/visible surface touched by this change — it's
  backend-only (DSN + repository-layer transaction handling), so the
  visual-check attestation this pipeline otherwise requires doesn't apply
  here; explicitly not skipped, just genuinely not applicable.

## Safe to merge

Yes. `go test ./... -race` run in full, twice (once by Dev, once
independently by the Reviewer, once more after the two post-review
comment fixes): every package green except one **pre-existing, unrelated**
failure — `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
(already tracked as its own card, ut-docs#258 — this sandbox runs tests as
root, which bypasses the read-only-directory permission check the test
relies on). Confirmed pre-existing by stashing this diff entirely and
re-running against a clean tree: identical failure. `internal/issuereport`
has no relationship to `internal/db`/`internal/data`; not touched by, and
not caused by, this change.
