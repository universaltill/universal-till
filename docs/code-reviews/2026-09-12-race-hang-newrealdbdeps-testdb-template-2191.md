# 2026-09-12 — `go test ./internal/pages/... -race` timeout: clone a once-built migrated template DB in `newRealDBDeps` (ut-docs#2191)

Reviewer: Opus (independent model — the change was authored by Fable, so the
review is deliberately not Fable), in an isolated worktree branched from
`fix/2191-race-hang-testdb-template` @ `39d15ea`.

## What shipped

One file, test-only: `internal/pages/demo_seed_opt_in_test.go` (+138/-1 at
`39d15ea`). **Zero production code changed** — `internal/db` is untouched.

`newRealDBDeps` used to run the entire migration chain from scratch on every
call (`db.Open(filepath.Join(t.TempDir(), "demo-optin.db"))`), 50 times per
test-binary process. It now:

1. Builds the fully-migrated schema **once per process** behind a
   `sync.Once` (`buildRealDBTemplate`): opens a throwaway DB under
   `os.MkdirTemp`, runs `PRAGMA wal_checkpoint(TRUNCATE)` to fold WAL content
   into the main file, closes it, reads the file's bytes into memory, and
   removes the throwaway directory before returning.
2. On every `newRealDBDeps(t)` call, writes those cached bytes to a fresh
   path under **that test's own** `t.TempDir()` and calls `db.Open` normally.

`db.Open`'s `migrate()` then sees the `schema_migrations` ledger already at
the newest version and runs only `verifyAppliedMigrations` — a per-migration
ledger `SELECT` plus a checksum compare — instead of re-executing the real
DDL and seed SQL. Genuine migration-file drift is therefore still caught on
**every single clone**, not just on the one template build.

Adds `TestNewRealDBDeps_TemplateClonesAreIsolatedAndFullyMigrated`, which
pins both halves: pointer identity on the returned slice (the `sync.Once`
cache is genuinely reused, not rebuilt) and that two `newRealDBDeps` calls
never see each other's writes.

## What the independent review found

### F1 — Call-site arithmetic in the new comment was wrong by >3x (Low, **fixed**)

The new comment block asserted the hang came from *"~80 from-scratch db.Open
calls (48 of them through newRealDBDeps alone)"*. Both numbers are wrong, and
the first is wrong in a way that matters.

`~80` is the count of `db.Open(` **source lines** in the package (77), not
runtime calls. Two of those 77 lines sit inside helpers that are invoked 188
times between them, so counting lines massively understates the real figure.
Measured properly, a full-package run performs **~263 fully-migrated opens**:

| Path | Sites | Status |
| --- | --- | --- |
| `newRealDBDeps` | 50 | fixed by this card |
| `openPagesTestDB` (`ui_smoke_test.go`) | 138 | **untouched** |
| ad-hoc `db.Open` across 46 other files | ~74 | untouched |

This matters for two reasons beyond tidiness. First, the follow-up Backlog
card will be scoped from this comment, and as written it implies roughly 30
stragglers remain when in fact ~212 do. Second — and more useful — it hides
that **`openPagesTestDB` alone is 138 call sites behind a single function**,
i.e. the largest remaining lever by a wide margin (2.8x this card's own), and
one that the *identical* technique fixes. The follow-up is therefore not "46
files of scattered cleanup"; it is "one more helper, then 46 files of
scattered cleanup". That reframing is worth having before the card is written.

Fixed in this branch: the comment now carries the corrected breakdown, the
measured A/B numbers below, and an explicit note that the earlier "~80" was a
source-line count. The commit message of `39d15ea` still carries the old
"~80 / 48 / ~70" figures; it is already pushed, so it was left alone rather
than force-pushed — this record is the correction of record.

### Checked and found clean (no finding)

- **Per-test isolation cannot leak.** Every call takes a *fresh*
  `t.TempDir()` (Go returns a new numbered directory per call on the same
  `t`, which the new test demonstrates empirically by calling
  `newRealDBDeps(t)` twice on one `t` and asserting non-visibility of
  writes). `os.WriteFile` opens `O_WRONLY|O_CREATE|O_TRUNC`, so it fully
  overwrites rather than appending. WAL/SHM sidecars are created by `db.Open`
  *next to the fresh path*, so no `-wal`/`-shm` from a prior crashed run can
  be adopted — there is no prior run at a path that did not exist a moment
  ago.
- **`migrate()` genuinely skips the real work** — read directly in
  `internal/db/db.go`. On a complete ledger it does: `CREATE TABLE IF NOT
  EXISTS` (no-op), one `MAX(version)` read, `checkSchemaLineage` (2 SELECTs),
  `loadMigrations()`, `verifyAppliedMigrations`, then an apply loop where
  every `m.Version <= current` and is skipped. No DDL, no seed SQL.
- **`verifyAppliedMigrations` is not itself expensive.** 27 migration files
  totalling 192 KB (post-ADR-0074 squash): 27 single-row SELECTs plus 27
  SHA-256 checksums over comment-stripped text, then one ledger scan. The
  measured saving (~60ms per avoided open without `-race`) is consistent with
  the ~52ms/migrated-open figure already benchmarked in `openPagesTestDB`'s
  own comment, i.e. the verify pass costs a small fraction of a real chain.
- **Error handling surfaces to the caller, never a panic or a hang.**
  `buildRealDBTemplate` returns errors on every path; `realDBTemplate` records
  the error once inside the `Once` and every subsequent caller reports it via
  `t.Fatalf` on its own test's goroutine. The explicit `len(b) == 0` check
  means a silently-empty template is an error rather than a 0-byte file that
  SQLite would happily treat as a fresh DB and re-migrate.
- **The `wal_checkpoint(TRUNCATE)` is correct, and safe even if it no-ops.**
  A `TRUNCATE` checkpoint can return busy (`1, -1, -1`) *without* an error,
  which `Exec` would not surface. It does not matter here: the subsequent
  `dbo.Close()` performs SQLite's last-connection checkpoint before the file
  is read, so the bytes are complete either way. The explicit pragma is
  belt-and-braces, not the sole guarantee.
- **The two bug classes this pipeline keeps finding are both absent.** No
  missing directory creation — `os.MkdirTemp` creates its own directory,
  `t.TempDir()` always exists, and `db.Open` runs `os.MkdirAll` on the parent
  regardless. No cwd-relative path — migrations are `//go:embed`'d
  (`internal/db/migration.go`), `os.MkdirTemp("")` and `t.TempDir()` both
  return absolute paths, so nothing depends on the cwd that `chdirRoot(t)`
  establishes. (The new test calls `realDBTemplate(t)` *before* any
  `chdirRoot(t)`; that is safe precisely because the migration set is
  embedded rather than read from disk.)
- **Clones are not silently sharing per-install identity.** Grepped the
  migration set for `randomblob`/`hex(random(`/`uuid`/`strftime` and
  non-default `datetime('now')` — none. The 14 seed `INSERT`s across 5
  migrations are all deterministic data, so a clone is byte-equivalent to a
  fresh migration except for `schema_migrations.applied_at`, which no
  production code reads (grepped: only a fixture in `internal/db/lineage_test.go`).
- **No data race on the shared template slice.** `sync.Once` establishes
  happens-before between the single writer and every reader, and all callers
  only read. 48 tests run clean under `-race`.
- **Repository-pattern rule respected.** No SQL text added outside
  `internal/data`/`internal/db` — `guard-data-access.sh` green. (Note the new
  test asserts "fully migrated" via `data.NewDemoSeedRepo(...).SampleItemCount`
  rather than a direct ledger query, which is the right call: a raw ledger
  `SELECT` in a test under `internal/` would fail that guard.)
- **Not a UI or user-facing change**, so the `web/help/` manual rule and the
  UX-guidelines checklist do not apply. No locale keys touched. No real
  client/shop name used as test data; no secret-shaped literals.

## What was verified beyond reading the diff

All commands run in the isolated review worktree.

**Static gates** — all green:

```
gofmt -l internal/pages/demo_seed_opt_in_test.go   -> (no output)
go build ./...                                     -> exit 0
go vet ./...                                       -> exit 0
golangci-lint run ./internal/pages/...             -> 0 issues
bash scripts/ci/guard-data-access.sh               -> ✓ no inline SQL outside internal/data / internal/db
```

**Full package** (`go test ./internal/pages/... -count=1`): PASS,
`internal/pages 225.664s` (plus catalog/common/itemsnav/settingsnav green).
Re-run green after F1's fix.

**Independent revert-then-restore A/B of the load-bearing claim.** Rather
than take "migrate() skips the chain" on the implementer's word, the helper
was reverted in-place to the exact pre-fix one-liner
(`db.Open(filepath.Join(t.TempDir(), "demo-optin.db"))`), measured, and
restored — verified clean afterwards by an empty `git status --porcelain`.
The subset is the 48 tests that actually call `newRealDBDeps`, with the new
isolation test excluded so the reverted configuration builds zero templates
(a true A/B rather than a handicapped one). Each configuration was compiled
warm first, then timed twice.

| Configuration | 48-test subset, no `-race` | 48-test subset, `-race` |
| --- | --- | --- |
| **With fix** (`39d15ea`) | **1.936s** (wall 3.462 / 3.504) | **20.512s** (wall 22.346) |
| **Reverted** (pre-fix) | 4.962s (wall 6.540 / 6.567) | 99.233s (wall 101.073) |
| Improvement | **2.56x**, −3.0s | **4.84x**, −78.7s |

The claim holds, and the `-race` column is the interesting one: ~1.6s of
`-race` time is saved per avoided migration chain, versus ~60ms without it —
a ~26x amplification, which is precisely the mechanism the card's
investigation described (cumulative instrumented overhead, not a deadlock).

A **single** test was also timed in both configurations, as a control:
`TestSetupWizardShopTypeAndDemoOptIn` took 1.744s with the fix and 1.746s
without it — indistinguishable, exactly as predicted, because one test still
pays for one migration chain either way (the template build). This is a
useful negative result: it confirms the win is strictly cumulative, and that
anyone A/B-ing this change on a single test would wrongly conclude it does
nothing.

**The goroutine-hang investigation's conclusion is corroborated.** Nothing in
this review found evidence of a deadlock, and the reading of `migrate()`
explains the observed symptom fully: the blame landing on a different test
each run, with a dump showing live SQLite DDL rather than a blocked
goroutine, is what cumulative overhead against a fixed deadline looks like.
No lock, channel or `WaitGroup` is involved in the path at all.

**The disclosed limitation was reproduced rather than assumed.** With the fix
in place:

```
go test ./internal/pages/... -race -count=1 -timeout=300s
  panic: test timed out after 5m0s
  FAIL  github.com/universaltill/universal-till/internal/pages  300.133s
```

So the full-package `-race` gate still does not complete. That is expected
and was disclosed up front — it is not a regression and not a new discovery —
but it is the fact the closing decision below turns on.

## Verdict

**Safe to merge.** The change is test-only, mechanically sound, measurably
effective, isolation-tested, and preserves the migration-drift detection that
made the from-scratch opens worth their cost in the first place. One Low
finding (F1, comment accuracy) was found and fixed in-branch. Nothing
deferred as a defect.

## Recommendation: `Refs`, not `Closes` — keep ut-docs#2191 open

This is the central question the card poses, and the answer is not close.

ut-docs#2191 is not a card about redundant migration work; it is a bug report
whose title and body are *"`go test ./internal/pages/... -race` is
unpredictably timing out"*. The acceptance criterion a reader will apply to
it is "does that command complete now". It does not — reproduced above, still
timing out at 300s, and extrapolating from the measured ~1.6s of `-race` cost
per migration chain against ~212 remaining opens, it is not close to
completing at 600s either. Closing #2191 would mean the next person to run
the exact command in its title hits the exact failure in its title against a
closed issue. That is the specific failure mode issue hygiene exists to
prevent, and no amount of accurate release-note prose in the PR body
compensates for it, because the person who hits it will search issues, not
changelogs.

The counter-argument — that the pipeline's own "split into sub-cards rather
than balloon one commit" rule justifies closing on a well-scoped partial — is
a good rule being applied to the wrong object. That rule governs how *work*
is split. It does not license marking a *symptom* resolved because a
contributing cause was. The correct application of the same rule here is: keep
#2191 as the symptom-level card, and let it be closed by its last
contributing sub-card. This one is genuinely well-scoped, genuinely
measurable (2.6x / 4.8x), and genuinely worth landing on its own — none of
that is in question. It is simply not the whole of what #2191 asks for, and
it removes under a fifth of the cost.

There is also a concrete, near-term reason to expect the remainder to be
cheap, which makes staying open low-cost: per F1, **138 of the ~212 remaining
opens are behind `openPagesTestDB`, a single function**, and the identical
template-clone technique applies to it essentially verbatim. The realistic
path to actually closing #2191 is one more card much like this one, plus a
longer tail. Holding the issue open across two cards is a small price;
reopening a wrongly-closed bug after a contributor rediscovers it is a larger
one.

Practically, for the PR:

- Word the reference as `Refs universaltill/ut-docs#2191` — which
  `39d15ea`'s commit message already does correctly.
- **Do not write a negated closing phrase** in the PR body. GitHub's
  merge-time scan is a plain substring match with no concept of negation, so
  "this does not close #2191" closes #2191 anyway (ut-docs#1609). Write
  "ut-docs#2191 stays open pending the `openPagesTestDB` follow-up" or
  similar, with no closing verb adjacent to the reference.
- File the follow-up Backlog card explicitly scoped as **`openPagesTestDB`
  first (138 sites, one function), then the ~74 ad-hoc sites across 46
  files** — not as one undifferentiated "the other ~70 call sites", which is
  the framing F1 corrected.
