# Code review: catalog item-update read-then-write race (ut-docs#1399)

## What shipped

`ut-docs#1399` is the server-side half of the duplicate-row bug whose
client-side half shipped as ut-docs#1365 — and it was filed *by* that card's
review as the part a client-side fix explicitly cannot reach (see
`docs/code-reviews/2026-09-02-catalog-double-submit-1365.md`, "Explicitly
deferred").

`/api/catalog/item/update` (`internal/pages/catalog/handlers.go`) decides its
HTMX OOB mode from the item's *previous* `is_active`: an inactive item has no
row rendered in the table, so a save that reactivates it must `beforeend`
APPEND its row, while an already-active item updates in place. That previous
state used to be read by a `repo.GetItem` call followed by a separate
`pos.UpdateItem` call — two unsynchronized steps. Two *genuinely* concurrent
requests on the same item (different tabs, sessions, or till clients — not one
operator's double-click, which #1365 already covers) could both complete the
read before either write landed, both see `wasActive=false`, and both emit an
append: two DOM rows sharing one id.

**Fix** (`internal/data/catalog_repo.go`, `internal/pos/catalog_ops.go`,
`internal/pages/catalog/handlers.go`):

- New `CatalogRepo.UpdateItemReturningWasActive` wraps the read and the write
  in a single `BeginTx`. The DSN carries `_txlock=immediate` (ut-docs#311), so
  that `BeginTx` runs `BEGIN IMMEDIATE` and takes the write lock **at BEGIN,
  before the read runs** — which is precisely what closes the window. A second
  concurrent caller blocks at BEGIN until the first commits, then reads the
  already-updated `is_active`, sees the row as active, and chooses in-place.
  This is the same idiom `AddBarcode` uses for its check-then-insert fix
  (ut-docs#304), including the `defer func() { _ = tx.Rollback() }()` that is a
  no-op (`sql.ErrTxDone`) after a successful Commit.
- `GetItem` / `UpdateItem` bodies were extracted into `getItemExec` /
  `updateItemExec` over the pre-existing `execer` interface (satisfied by both
  `*sql.DB` and `*sql.Tx`, already used by `ensureInventoryRowExec` /
  `CreateItemTx`), so the same statements run under either handle without a
  second copy of the query text.
- `pos.UpdateItemReturningWasActive` thin wrapper; handler switched to the
  single call.
- New concurrency test `internal/data/catalog_repo_update_item_race_test.go`:
  8 concurrent updates per round × 15 rounds against a **real migrated
  file-backed** DB via `db.Open` (an in-memory DSN gives each pooled connection
  its own isolated database and cannot exercise multi-connection locking at
  all — same reasoning as `TestAddBarcodeConcurrentRace`), asserting exactly one
  caller ever sees `wasActive=false`.

## Review

Independent review by a different model (Opus) in a fresh context that never
saw the dev reasoning. Findings below were fixed in the review pass.

**Verdict: no blocker issues. Two minor findings fixed, one CI finding
reported rather than fixed (scope decision by the coordinator).**

### Correctness — verified, not assumed

- **Does `BEGIN IMMEDIATE` actually serialize as claimed?** Yes, and this was
  checked two ways rather than taken on faith. Statically: `internal/db/db.go`
  builds the DSN with `_txlock=immediate`, whose own comment documents exactly
  this failure mode (a deferred BEGIN's SHARED→RESERVED promotion skips the
  busy handler, so `busy_timeout` never applies and the read-then-write shape
  fails instantly with SQLITE_BUSY). Empirically: the TDD re-verification below
  shows the test goes red the moment the transaction is removed and green with
  it, which is only possible if the lock is genuinely taken before the read.
- **Conservative default preserved faithfully.** The pre-fix handler read
  `wasActive := true; if prev, ok, err := repo.GetItem(...); err == nil && ok {
  wasActive = prev.IsActive }` — a read *error* (as distinct from a clean "not
  found") is swallowed and only the write's error fails the request. The new
  method reproduces that byte-for-byte against `tx`. Confirmed identical.
- **Item-not-found path unchanged.** A non-empty id with no matching row:
  `getItemExec` returns `ok=false`, `wasActive` stays at the conservative
  `true`, the UPDATE matches zero rows and returns no error, so the handler
  emits an in-place update for a row that isn't there — exactly what it did
  before this card. Not a regression; now pinned by a test (below).
- **Error paths.** `BeginTx` failure and `Commit` failure are both wrapped and
  returned (`update item: begin/commit: %w`), and the handler's
  `skuAwareError(... 400 ...)` treatment is unchanged. Returning `true` as the
  bool alongside an error is inert (the handler returns early) and is the
  conservative value anyway. `tx.Rollback()` after a successful Commit returns
  `sql.ErrTxDone` and is discarded — correct, same as `AddBarcode`.
- **No behaviour change beyond the race fix.** `getItemExec`/`updateItemExec`
  have exactly two callers each — their own public wrapper and the new atomic
  method — and the extracted bodies are unchanged apart from the receiver→
  `execer` swap, so `GetItem`/`UpdateItem`'s existing callers are untouched.
  `ErrSKUExists` mapping via `isUniqueViolation` still fires inside the tx and
  still reaches `skuAwareError`.
- **Handler-level regression cover already exists and passes:** `row_oob_test.go`
  asserts both branches — reactivation must append and must *not* emit an
  in-place update (`:151`/`:155`), and an already-active update must not append
  (`:181`). Those are the tests that would catch a broken OOB decision, and
  they pass unchanged against the new call.

### Findings fixed in this pass

- **Minor (fixed) — validation ran inside the transaction.** `updateItemExec`
  validates `id required` / `name required`, and the new method called it only
  *after* `BeginTx`. Since `BEGIN IMMEDIATE` takes the database-wide write lock
  at BEGIN and waits up to `busy_timeout(5000)` for it, a malformed request
  (both cases are reachable from the form) would queue behind a live sale's
  writer for up to five seconds — and then hold that lock itself — purely to be
  told "id required", which needs no database access at all. `UpdateItem`'s
  own behaviour was unaffected, but on an offline-first till that shares this
  write lock with checkout, taking it to reject a request that is invalid on its
  face is the wrong order of operations. Fixed by extracting the two checks into
  `validateItemUpdate(in)`, called by `updateItemExec` (unchanged behaviour and
  identical error text) and by `UpdateItemReturningWasActive` *before* `BeginTx`.
- **Nit (fixed) — doc comment grammar.** `UpdateItemReturningWasActive`'s doc
  opened a sentence with "Read outside the write (the original shape: …), two
  genuinely concurrent updates can both read…" — a dangling clause. Now "With
  the read outside the write (…)". The rest of the comment's technical claims
  were checked against `db.go` and `AddBarcode` and are accurate as written.
- **Nit (fixed) — inaccurate cross-reference.** The new test's doc said its
  real-DB rationale was "same reasoning as `TestAddBarcodeConcurrentRace`
  **above**", but that test lives in a different file
  (`catalog_repo_concurrency_test.go`). Now named explicitly.

### Coverage added in this pass

The race test asserts the concurrent invariant but says nothing about
single-caller semantics, so a future refactor could change what the OOB
decision is derived from without any test objecting. Added
`TestUpdateItemReturningWasActiveSemantics` to the same file, pinning the four
cases against what the pre-fix handler did: previously-inactive → `false`
(append), previously-active → `true` (in place) with the write confirmed
committed, unknown id → conservative `true` and no error, and empty id / empty
name → rejected.

### Conventions

Repository pattern holds: every statement this card adds or moves lives in
`internal/data`, the handler and `internal/pos` only call methods.
`guard-data-access.sh` re-run and passes. No new user-facing string, no locale
key, no kiosk-engine reference, no compliance wording, no money handling, no
migration — the i18n/compliance/money rules have no surface here.

## TDD verification (independently re-run, red then green)

The claim was not taken on trust. `UpdateItemReturningWasActive` was reverted
in the working tree to a naive non-atomic two-step (the pre-fix shape: a
`getItemExec` against `r.db`, then an `updateItemExec` against `r.db`, no
transaction), leaving everything else in place:

```
$ go test -count=1 -run TestUpdateItemReturningWasActiveConcurrentRace ./internal/data/
--- FAIL: TestUpdateItemReturningWasActiveConcurrentRace (0.22s)
    catalog_repo_update_item_race_test.go:86: round 1: expected exactly 1 of 8
      concurrent updates to see wasActive=false (the single legitimate append),
      got 2 — duplicate-row race
FAIL

$ # reliably red, not a one-off — 3 consecutive runs:
--- FAIL (0.21s) / --- FAIL (0.21s) / --- FAIL (0.19s)
```

Red for exactly the right reason: two callers taking the append branch, which
is the duplicate row. Restored with `git checkout -- internal/data/catalog_repo.go`
(and re-applying the review fixes above):

```
$ go test -count=1 -run TestUpdateItemReturningWasActive ./internal/data/ -v
--- PASS: TestUpdateItemReturningWasActiveConcurrentRace (0.72s)
--- PASS: TestUpdateItemReturningWasActiveSemantics (0.16s)
ok  0.881s          # green on 3 consecutive runs

$ go test -count=1 -race -run TestUpdateItemReturningWasActive ./internal/data/
ok  10.696s         # no data race
```

`-race` was scoped to the new tests deliberately: a full-suite `-race` run is
known to time out in this sandbox (ut-docs#1366/#1394).

## Full gate

```
gofmt -l .                                    → empty
go build ./...                                 → clean
go vet ./...                                   → clean
go test ./internal/data/...                    → ok, 78.485s
go test ./internal/pos/...                     → ok,  4.780s
go test ./internal/pages/catalog/...           → ok,  0.503s
go test ./...        (full suite, no -race)    → all packages ok, zero failures
bash scripts/ci/guard-data-access.sh           → pass
```

All figures above are from the final tree state (review fixes applied).

## `guard-docs-shots.sh` — resolved, folded into this PR (post-review update)

The review pass above found `scripts/ci/guard-docs-shots.sh` red *because of*
this diff — confirmed not pre-existing (a clean-tree stash-and-rerun passes)
— and, as a coordinator scope call at review time, reverted the regenerated
screenshots to keep the diff Go-only, intending a separate follow-up card.
On reflection that call was wrong: `guard-docs-shots.sh` is one of the
CI-blocking gates this repo's `CLAUDE.md` explicitly lists under "Before
committing," this diff is what invalidates it (the guard hashes
`internal/pages/catalog/handlers.go` as a whole file, and that file registers
the screenshotted `/catalog` route — a comment/signature-only edit still
changes the file's hash), and deferring a guard THIS diff breaks to a future
card would just leave `main` red the moment this merges. So `make docs-shots`
was re-run and its output **is included in this commit**:

```
$ make docs-shots            → 96 passed, wrote web/help/img/manifest.json
$ bash scripts/ci/guard-docs-shots.sh
✓ docs-shots guard: 24 routed topics × 4 locales screenshotted and fresh (surface 4e905bb14653…)
```

Changed: `web/help/img/manifest.json` plus 4 regenerated PNGs (which 4 vary
slightly run-to-run — timing-dependent rendering noise in the screenshot
tool itself, not a code effect; confirmed by running `make docs-shots` twice
and getting a different PNG subset each time with the guard green both
times). No `web/ui`/`web/public` markup changed, so this is pure
regeneration noise from `handlers.go`'s hash moving, not a real UI change to
review.

## Safe-to-merge verdict

**Yes.** The race is genuinely closed, verified red-then-green by an
independent re-run rather than accepted on the author's word; the
conservative read-error default and the item-not-found behaviour are
preserved exactly; no existing caller of `GetItem`/`UpdateItem` changes
behaviour; the two minor findings (validation inside the lock, doc
inaccuracies) were fixed in this pass; and the `guard-docs-shots` gate this
diff invalidates is now included and green, not deferred. Full gate
(including `guard-docs-shots.sh`) is green on the final tree.

## Follow-ups

- Worth considering separately (not this card's problem to fix): the guard's
  whole-file hashing of any `internal/pages/**.go` that registers a
  screenshotted route means every purely-backend edit in such a file forces
  a 96-screenshot regeneration. The guard's own header already documents an
  analogous over-inclusion trade-off as accepted; this instance is another
  data point for it, not a defect in this card.
