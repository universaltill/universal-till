# Code review: report_archive sequential Z-number + receipt range (ut-docs#1080)

Reviewer: independent second-opinion pass (Opus), fresh context, no part in
writing the change. The implementation commit (`2fdf5c6`) was written by a
different model (Fable); this review was carried out in an isolated worktree
checked out at that commit, against `origin/main`.

## What shipped

`report_archive` had no numeric sequence — a row was identified only by
`(kind, period)` (migration 013's `UNIQUE (kind, period)`), so nothing tied
one close to the one before it. This card adds, purely additively:

- **Migration `070_report_archive_zreport_numbering.sql`** — five nullable
  columns (`z_number`, `prev_z_number`, `prev_closed_at`, `first_receipt`,
  `last_receipt`) plus a **partial** unique index
  `ux_report_archive_kind_znumber ON report_archive (kind, z_number) WHERE
  z_number IS NOT NULL`.
- **`POSRepo.ArchiveReport`** — now allocates a sequential, gapless
  Z-number per `kind` and chains each close to its predecessor
  (`prev_z_number`, `prev_closed_at`), in ONE atomic `INSERT ... SELECT
  COALESCE(MAX(z_number),0)+1 ... ON CONFLICT (kind, period) DO NOTHING`,
  with a 3-attempt retry. Signature grows two parameters
  (`firstReceipt`, `lastReceipt`).
- **`ArchivedReportRow` + `scanArchivedReport`** — the five new columns are
  read back by both `ListArchivedReports` and `ArchivedReportsInRange`, with
  the two nullable "predecessor" facts modelled as pointers.
- **`generateEOD`** passes `rep.FirstReceipt`/`rep.LastReceipt` (already
  computed as `MIN/MAX(receipt_no)` in `dateRangeSummary`) through, so the
  receipt range becomes queryable instead of buried in `content_json`.
- Tests: a new `internal/data/pos_repo_zreport_test.go`, an assertion added
  to `TestGenerateEOD_ArchivesOnceThenIdempotent` so the AC is proven through
  the real production path, a `rewindZReportNumbering070` helper wired into
  all 12 migration-replay test sites, and the hand-rolled `report_archive`
  schema in `ui_smoke_test.go` brought back in sync.

This is the pattern `InvoiceRepo.Create` already established for
series-scoped invoice numbering (INSERT...SELECT COALESCE(MAX)+1, DB-level
unique index, 3x retry), and it follows it faithfully.

**Scope check — ADR-0057 is untouched.** The diff does not modify
`dateRangeSummary`, the `date(created_at, 'localtime')` window, or how
`generateEOD` computes `day`. `period` is still the shop's local calendar
date and `(kind, period)` is still the write-once identity. The sibling card
ut-docs#1081 owns any change there and would need its own superseding ADR.

## Verdict

**Safe to merge.** No functional defect found. Three low-severity findings,
all fixed in this review's follow-up commit (two comment-accuracy fixes and
two added tests). Two further observations accepted as-is and recorded below.

## What I verified beyond the automated tests

I did not take the SQL's stated semantics on trust. Every claim below was
checked with a throwaway probe program built against the repo's real driver
(`modernc.org/sqlite`) and, where it mattered, the repo's real DSN
(`internal/db.Open`: `foreign_keys(1)`, `busy_timeout(5000)`,
`journal_mode(WAL)`, `_txlock=immediate`). The probe lived in a scratch
package inside the worktree and was deleted afterwards — `git status` is
clean of it.

### 1. The atomic allocation SQL's edge cases

- **Can a legacy (pre-migration, NULL `z_number`) row corrupt the chain?**
  No. Measured: with two `NULL`-`z_number` rows of `kind='eod'` already
  present, the first `ArchiveReport` call produced `z_number=1`,
  `prev_z_number=NULL`, `prev_closed_at=NULL`. Both halves of the chain
  exclude legacy rows correctly and for *independent* reasons —
  `MAX(z_number)` skips NULLs, and the `prev_closed_at` subquery filters
  `z_number IS NOT NULL` explicitly. That redundancy is deliberate and
  right: the two would otherwise be able to disagree.
- **A legacy-shaped row inserted by some other code path after 070 ships,
  interleaved with real numbered closes?** It would land with
  `z_number=NULL` and simply be invisible to both the `MAX` and the
  subquery — it cannot be picked up as a predecessor and cannot displace a
  number. The chain stays intact; the unnumbered row is just not part of it.
  I confirmed there is no such other path today: `ArchiveReport` is the only
  writer of `report_archive` in production code (single caller,
  `generateEOD`), and `guard-data-access.sh` structurally prevents an
  `INSERT` appearing outside `internal/data`.
- **Does `MAX(z_number)` really ignore NULLs in this bare-aggregate,
  no-`GROUP BY` shape?** Yes — measured, not assumed. An aggregate without
  `GROUP BY` yields exactly one row even over zero matching rows, and
  `MAX` returns `NULL` there, which `COALESCE(...,0)+1` turns into 1. Note
  also that the `SELECT` carries a `WHERE` clause, which is what avoids
  SQLite's documented `INSERT...SELECT` / `ON CONFLICT` parsing ambiguity —
  it happens to be required here anyway for the `kind` scoping, but it is
  load-bearing twice over.
- **Is the partial unique index actually enforced by this build/driver?**
  Yes — measured. A second `(kind, z_number) = ('eod', 2)` row was rejected
  with `UNIQUE constraint failed: report_archive.kind,
  report_archive.z_number (2067)`, while `('shift', 2)` alongside it was
  accepted (correct kind-scoping) and multiple `NULL`s coexisted (correct
  partiality). I also confirmed `ON CONFLICT (kind, period) DO NOTHING`
  does **not** swallow that violation: with the conflict target set to
  `(kind, period)`, a `z_number` collision still surfaces as an error. That
  matters — if `DO NOTHING` had absorbed it, the retry loop would be
  unreachable and a lost race would silently return `created=false`.

### 2. `prev_closed_at` timestamp handling

Clean, no double-formatting and no timezone drift.

The subquery copies the predecessor's **raw** `created_at` column value.
Measured: what lands in `prev_closed_at` is `"2026-08-25 19:01:05"` — the
same `datetime('now')` shape `created_at` itself carries, never a
pre-formatted value. On read, `scanArchivedReport` runs it through the *same*
`formatArchiveTimestamp` as `created_at`, so both become RFC3339. The
`.UTC()` call is a no-op rather than a shift, because `time.Parse` with a
zone-less layout already yields UTC and `datetime('now')` is UTC — the two
assumptions line up. `TestArchiveReport_SecondCloseChainsToFirst` asserts
`second.PrevClosedAt == first.CreatedAt` *after* both have been through the
formatter, which is the right end-to-end assertion (it would catch a
one-sided formatting change).

Residual: `created_at` is second-resolution, so two closes in the same
second give a predecessor timestamp equal to the successor's. Harmless — the
`z_number` chain, not the timestamp, is what orders the sequence.

### 3. The 3x retry loop's error handling

My independent view: **the blanket retry is acceptable, but the comment
justifying it was wrong, and that was worth fixing.**

The orchestrator's framing was that this differs from `InvoiceRepo.Create`,
which breaks out of its loop on a `sale_id` conflict. That difference is
correct and *appropriate*: `InvoiceRepo`'s `UNIQUE(sale_id, kind)` is a real
"already invoiced, never retry" signal that arrives as an error, whereas
here the analogous case — a duplicate `(kind, period)` — is absorbed by
`DO NOTHING` and returns `created=false, nil` rather than an error. There is
genuinely no second conflict class to filter on. So the shape is right.

Nor does the blanket retry mask a real bug. A malformed query, a schema
mismatch or a cancelled context fails identically on all three attempts and
the *original* error is returned wrapped (`archive report: %w`) — the
message a developer needs is still the message they get. The cost is two
extra immediate no-op round trips on an already-failing path.

What I did find is that the comment claimed **"Any error is therefore a lost
race on the `ux_report_archive_kind_znumber` UNIQUE index"** — which is not
true. I/O errors, busy-timeout expiry and context cancellation all reach that
line. Worse, I measured that the lost race the comment names as the *only*
possibility does not actually occur on this DSN: **60 concurrent
single-attempt inserts (retry removed), real DSN, pooled connections,
produced 60 gapless numbers and zero errors, twice.** The reason is that an
autocommit `INSERT ... SELECT` takes SQLite's write lock *before* it
evaluates the `SELECT`, so `MAX()` is read under that lock and concurrent
closes serialise rather than race. (`_txlock=immediate` is irrelevant here —
it applies to `BeginTx`, not to autocommit statements.)

So the retry is defence in depth against a future change of shape (an
explicit deferred transaction, another driver), not a response to an observed
race. That is a perfectly good reason to keep it — but the comment should say
so, and the concurrency test's comment should not claim to exercise a path it
never reaches. **Fixed** (finding F1/F2 below).

### 4. Return-value semantics — can a retry-exhausted error hide a partial write?

**No, confirmed rather than asserted.** Each attempt is a single statement in
autocommit, i.e. its own implicit transaction, so a constraint failure rolls
the whole statement back. I forced the collision with a `BEFORE INSERT`
trigger that also wrote a second row, and after the failure **both** rows
were absent (`COUNT(*) = 0`) — the trigger's side effect was rolled back
along with the insert. A caller that sees `(false, err)` can rely on "no row
of mine was written".

The converse direction is also sound, and is the subtler one. Suppose
attempt 1 actually commits but the driver returns an error afterwards
(a deadline firing post-commit). Attempt 2 then hits the `(kind, period)`
conflict, `DO NOTHING` fires, `RowsAffected` is 0, and the function returns
`(false, nil)` — the error is "swallowed". That is the *right* answer: a
`RowsAffected == 0` outcome can only mean the `(kind, period)` row exists, so
"already archived" is factually true. `generateEOD` then correctly skips the
duplicate `eod_generated` audit write. There is no path that returns
`(false, nil)` with no row present.

### 5. General code quality

Idiomatic and consistent with the surrounding file, including its unusually
high comment density. `scanArchivedReport` is a good extraction — it removes
the duplicated scan/format between the two list methods, and its null
handling is correct: `sql.NullInt64`/`sql.NullString` for the nullable
columns, pointers for the two facts where NULL is genuinely distinct from a
value, plain `""`/`0` for the two where it is not. `ZNumber int64` reading as
0 for a legacy row is unambiguous (real numbers start at 1) and documented on
the struct.

I checked for missed construction/read sites: `ArchivedReportRow` is built
in exactly the two updated list methods and nowhere else, the two `SELECT`
column lists match `scanArchivedReport`'s argument order exactly, and there
is only one hand-rolled `report_archive` schema in the tree
(`ui_smoke_test.go`) — which was updated, index included. `ArchiveReport` is
not part of any interface or mock. The new export fields are snake_case and
ISO-8601, per this repo's API rules.

### 6. Demo data, secrets, and the UI/manual question

- **No real client or shop name** appears as demo/seed/test data anywhere in
  the diff. Test fixtures are `R001`/`R009`/`R010`/`R042` receipt stubs,
  `legacy1`/`legacy2` ids and 2025-2026 calendar dates.
- **No secret-shaped literals** — no keys, tokens, connection strings or
  credentials, and nothing is logged from this path.
- **The diff genuinely has no user-facing surface, and I confirmed it rather
  than assuming it.** `git diff origin/main...HEAD -- web/ docs/` is empty;
  no locale key is added or changed; `grep` across `web/` finds no reference
  to `report_archive`, `content_json` or the export's field names, so no
  `web/help/` topic documents a schema that this change drifts. Nothing new
  is rendered on the Reports page — the struct gained fields, the templates
  did not. `guard-i18n.sh` passes untouched. **So the UX and
  manual-update requirements in the reviewer skill do not apply to this
  card, and I am skipping them deliberately, not silently.** If a later card
  surfaces the Z-number to the shop owner, that card owns the help topic and
  the screenshot.

## Findings

### F1 — `ArchiveReport`'s doc comment misstated the error semantics (Low, **fixed**)

"Any error is therefore a lost race on the `ux_report_archive_kind_znumber`
UNIQUE index between two concurrent closes" is false: I/O errors,
busy-timeout expiry, a cancelled context and a schema mismatch all arrive at
the same line, and — as measured above — the named race does not actually
occur on this DSN at all. A future maintainer reading that comment could
reasonably conclude that errors here never need inspecting, or delete the
retry as untestable without understanding what it guards.

Rewritten to state what is true: what `DO NOTHING` absorbs, why the retry is
defence in depth rather than a response to an observed race (with the
measurement and the write-lock reason recorded), and explicitly that other
error classes are *not* masked. Comment-only; no behaviour change.

### F2 — the concurrency test claimed to exercise a path it never reaches (Low, **fixed**)

`TestArchiveReport_ConcurrentClosesAreGaplessAndUnique`'s comment said "this
is what exercises the lost-race retry path". It does not — measured zero
lost races out of 60 attempts under harsher contention than the test's 10.
The test is still valuable: it pins the end-to-end invariant that concurrent
closes never duplicate, skip or reorder a number. The comment now says that
instead, and points at `ArchiveReport`'s doc for why the retry is untaken.

### F3 — the DB-level guarantee was untested (Low, **fixed**)

Every test in the new file goes through `ArchiveReport`, which allocates
correctly on its own. If `ux_report_archive_kind_znumber` were ever
downgraded to a plain non-UNIQUE index — a plausible "cleanup" edit, since
nothing in Go depends on it failing — **all of them would still pass**, while
the database quietly stopped rejecting a duplicate Z-number arriving any
other way (a restore, a repair script, a future writer). The uniqueness
constraint is the actual backstop for the card's headline claim and deserved
a test of its own.

Added two tests:
- `TestArchiveReport_ZNumberUniquenessIsEnforcedByTheDatabase` — writes
  straight past the repository to assert the constraint rejects a duplicate
  `(kind, z_number)`, and that the same number under a different kind is
  accepted.
- `TestArchiveReport_SequenceIsScopedPerKind` — pins that a second report
  kind starts its own sequence at 1 with no cross-kind predecessor, which is
  what both the `WHERE kind = ?` scoping and the index shape promise, and
  what 013's own "weekly/monthly later" comment anticipates.

### F4 — retention prune interacts with the "gapless" claim (accepted, documented)

`PruneReportArchiveOlderThan` (ADR-0040 §2, 10 calendar years) deletes old
numbered rows, so the *stored* sequence can start above 1. Measured: after
pruning `z=1,2` the next close correctly continued at 4, so allocation is
unaffected. But if a prune ever removed **every** numbered row of a kind, the
next close restarts at 1 and previously-issued numbers become reusable —
measured, and only reachable on a till dormant for the entire 10-year
retention window.

Not a defect of this card: the retention rule predates it and is ADR-0040's
call, and no realistic till reaches that state. But "gapless" is a fiscal
claim, so I added a sentence to `ArchiveReport`'s doc making explicit that
"gapless" describes *allocation* — no number skipped or reused while the rows
exist — and naming the retention interaction. Doc only; no behaviour change,
and no widening into `PruneReportArchiveOlderThan` itself.

### F5 — two adjacent same-typed parameters (accepted, no change)

`ArchiveReport(ctx, kind, period, content, firstReceipt, lastReceipt)` ends
in two bare `string`s that the compiler would let a caller swap silently.
With exactly one production caller, passing two adjacent struct fields in
their natural order, on a line whose comment names both, the risk is small
and a params struct would be heavier than the problem. Left as-is; noted so
a future third parameter prompts a rethink.

### F6 — sequence order is close order, not period order (accepted, no change)

`z_number` is assigned in insertion order while `ListArchivedReports` orders
by `period`. These coincide today because both callers
(`eodSchedulerTick` and `POST /api/reports/eod/run`) pass
`time.Now().Format("2006-01-02")` — there is no backdated-close path. A
future "generate the EOD I missed last Tuesday" feature would make the two
orders diverge, which is arguably still correct for a Z-number (it numbers
closes, not calendar days) but would want stating deliberately. Flagged for
whoever builds that, not changed here.

## Checks run (in the review worktree, at `2fdf5c6` and again after the fixes)

- `gofmt -l .` — clean.
- `go build ./...` — clean. `go vet ./...` — clean.
- `go test -count=1 ./...` — full suite green.
- `go test -race -timeout 45m -count=1 ./internal/data/... ./internal/pages/...`
  — green. (This package pair legitimately runs ~20-25 minutes under `-race`
  on a clean base, unrelated to this diff; the generous timeout is required,
  and a default 10m timeout here is a false "hang", not a real one.)
- `go test -race -count=1 -run ArchiveReport ./internal/data/...` — green,
  re-run after the added tests.
- **Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job**,
  not just the two the brief named — `guard-data-access.sh` (pass: all new
  SQL is in `internal/data` / `internal/db`), `guard-i18n.sh` (pass: no
  locale surface touched), plus `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-compliance-claims`, `guard-docs-shots`,
  `guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `check-brand-assets`
  and `guard-makefile-version` — all pass. `guard-help-topics` passing is
  the mechanical confirmation that this change adds no page route needing a
  manual topic.

## Fixes committed by this review

Comment accuracy in `internal/data/pos_repo.go` (F1, F4) and
`internal/data/pos_repo_zreport_test.go` (F2), plus two new tests in
`internal/data/pos_repo_zreport_test.go` (F3). No production behaviour
changed. The original commit `2fdf5c6` was left intact; the fixes are a
separate commit on top.
