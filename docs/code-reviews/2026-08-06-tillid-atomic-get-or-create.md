# 2026-08-06 — discovery.TillID atomic get-or-create (ut-docs#271)

Card: [ut-docs#271](https://github.com/universaltill/ut-docs/issues/271) (p3, medium)
Branch: `fix/271-tillid-atomic-get-or-create`

## What shipped

`discovery.TillID` (`internal/discovery/discovery.go`) was a plain
read-then-write get-or-create: two concurrent callers on a fresh DB (no id
persisted yet) could each miss the `Get`, each generate a different uuid,
and both `Set` — last write wins, and the caller holding the losing uuid
still returned it. This till id is shared between the mDNS advertiser's TXT
record and `derivedVerificationCode` (`internal/pages/pairing_api.go`); a
divergence there breaks pairing-code agreement between a primary and a
would-be replica.

- Added `SettingsRepo.GetOrCreate(ctx, key, ifAbsent string) (string, error)`
  (`internal/data/settings_repo.go`) — `INSERT INTO settings ... ON
  CONFLICT(key) DO NOTHING` followed by `SELECT value`, both inside one
  transaction, so concurrent callers converge on whichever `INSERT` actually
  landed. Raw SQL stays inside `internal/data`, per the repository-pattern
  rule.
- `discovery.TillID` now does a lock-free `Get` first (fast path for the
  steady-state case — this id changes at most once per install, and
  `pending_pairings.go`'s manager UI polls it every 30s) and only falls
  through to `GetOrCreate` when the id doesn't exist yet, which is the only
  case the race can actually occur in.
- Regression tests use a REAL file-backed SQLite DB
  (`db.Open(filepath.Join(t.TempDir(), ...))`), not `:memory:` — this
  driver/codebase gives every pooled connection to a `:memory:` DSN its own
  isolated database (same reason `TestAddBarcodeConcurrentRace`,
  ut-docs#304, uses a file-backed DB), so `:memory:` can't exercise real
  cross-connection contention at all. Both new tests use a start-gate +
  multi-round pattern (30 rounds × 8 concurrent callers) mirroring
  `TestAddBarcodeConcurrentRace`:
  `TestSettingsRepo_GetOrCreate_ConcurrentCallersConverge` (data layer) and
  `TestTillID_ConcurrentCallersOnFreshDBConverge` (the actual named
  function).

## Independent review (Opus, fresh context — medium-tier card)

Ran build/vet/the data-access guard/the full race-enabled suite itself, and
independently re-verified the TDD claim: reverted `GetOrCreate` to a naive
`Get`-then-`Set`, confirmed both new concurrency tests fail (reproducing two
different uuids surviving into two different callers — the actual #271
bug), restored the real implementation, confirmed both pass again, then
re-ran each 3× more (`-race -count=3`) to check for flakiness in the other
direction — clean every time.

**Verdict: safe to merge with minor notes.** No blockers. One finding
worth recording in detail because it's genuinely non-obvious:

- **The `INSERT`-before-`SELECT` ordering inside the transaction is
  load-bearing, not stylistic.** The DSN this codebase uses
  (`internal/db/db.go`) sets no `_txlock`, so `BeginTx` opens a plain
  **deferred** transaction. A deferred tx that reads first takes only a
  SHARED lock; if a second concurrent caller's transaction then needs to
  upgrade SHARED→RESERVED while another connection already holds RESERVED,
  SQLite treats that specific case as a locking conflict its busy handler
  does **not** retry — the "wait up to `busy_timeout`" behavior this whole
  fix depends on doesn't apply. Writing first means the transaction takes
  RESERVED immediately at the `INSERT`, so a losing connection just waits on
  `busy_timeout` normally instead of failing outright. Added an explicit
  comment on `GetOrCreate` recording this, since a future "read first, skip
  the pointless write" tidy-up would silently reintroduce it.

Two real-but-minor findings fixed in this same change (both cheap, both
in-scope for the file already being touched):

- `TillID` took a database-wide write lock on every call, even once the id
  already existed (the common case, and polled every 30s by the manager's
  pending-pairings view) — added the `Get` fast path described above.
- `uuid.NewString()` was evaluated as a function argument on every call
  (Go evaluates eagerly), i.e. a wasted uuid generation on almost every
  call — resolved by the same fast path, since `GetOrCreate` (and its
  `uuid.NewString()` argument) is now only reached on the absent path.

One nitpick fixed: the new `internal/data` import in
`settings_repo_test.go` shadowed pre-existing local `db :=` variables in
two of the new tests (compiled fine, but a trip hazard) — renamed to
`sqlDB` in those two, matching the convention `discovery_test.go` already
used for the same collision. Pre-existing tests in the same file weren't
touched, to keep the diff scoped to what this card actually changed.

**1 finding explicitly deferred, not fixed, per the reviewer's own
recommendation:**

- `GetOrCreate` returns whatever is stored verbatim, including a
  pre-existing empty/whitespace value — the old code's `Get`-then-`Set`
  treated a blank stored value as "absent" and regenerated it;
  `GetOrCreate` does not. Reviewer confirmed this is **unreachable in
  production today**: `lan_discovery.till_id` appears nowhere else in the
  repo, no `SetMany` caller writes it, and no settings import/restore
  feature exists that could write a blank value in the first place. Left
  as-is rather than adding untested defensive behavior for a path nothing
  can currently reach; would need re-examining if a settings import/restore
  feature ever ships.

**Also explicitly out of scope, noted for the backlog rather than folded
in:** the DSN-wide `_txlock=immediate`/WAL-mode question this same review
touched on is exactly [universaltill/ut-docs#311](https://github.com/universaltill/ut-docs/issues/311), already its own
card — not duplicated here.

## Verification

- `go build ./... && go vet ./...`, `bash scripts/ci/guard-data-access.sh`
  — all clean (`✓ data-access guard: no inline SQL outside internal/data /
  internal/db`).
- `gofmt -l` on every changed file — clean.
- `go test ./internal/data/... ./internal/discovery/... ./internal/pages/...
  -race -count=1` — all green (`internal/pages` included because
  `pairing_api.go`/`pending_pairings.go` call `TillID` against hand-built
  test DBs — confirmed the added `BeginTx` in the absent-path doesn't
  regress them).
- Full `go test ./... -race`: every package green except the pre-existing,
  unrelated `internal/issuereport` `TestSaveCleansUpDirectoryOnWriteFailure`
  (ut-docs#258, sandboxed root-run quirk — not touched by this change).
- TDD claim independently re-verified twice (once by the implementer, once
  by the reviewer, each reverting to a naive `Get`-then-`Set` and confirming
  both new tests fail before confirming they pass against the real fix).

## Safe-to-merge verdict

Yes. Independent review found no blockers; the one real-but-minor findings
were cheap enough to fix in this same change, and the one deferred finding
is confirmed unreachable in production today.
