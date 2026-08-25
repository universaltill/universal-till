# Code review: unique migration version numbers (ut-docs#1056)

## Change

Two concurrent pipeline lanes had independently created
`internal/db/migrations/067_*.sql` files from a similar branch point
(`067_vouchers.sql`, `067_shift_cash_reconciliation.sql`) — caught by
hand during ut-docs#1008's pre-merge rebase and fixed there by
renumbering, but nothing structural stopped it happening again and
landing straight to `main` if two such PRs merged in the same cycle.
`internal/db/migration.go`'s `loadMigrations` sorts by a leading integer
`Version` parsed from the filename with no defined tie-break for a
duplicate, and `internal/db/db.go`'s applier tracks progress by a
**high-watermark** `MAX(version)` check (`version <= current` → skip) —
so a collision doesn't fail loudly, it silently and permanently skips
whichever file loses the sort, on some fraction of installs, in a
fiscal/compliance-relevant schema.

Fix, both belt and braces:

- `scripts/ci/guard-migration-version-collision.sh` (+ its own
  `_test.sh`) — PR-time shell guard failing on a duplicate leading
  version number, wired into `.github/workflows/ci.yml`'s `build` job.
- `internal/db/migration.go` — `loadMigrations` now hard-fails
  (`checkNoDuplicateVersions`) on a duplicate, so the check can never be
  bypassed by skipping a script; it's the loader every till actually
  runs. Refactored to delegate to `loadMigrationsFromFS(fs.FS, dir)` so a
  test can inject a fixture directory with a real collision (`//go:embed`
  can't be fed a fake one).
- `internal/db/migration_test.go` — unit tests for the isolated check,
  plus wiring tests proving `loadMigrations`'s real path actually calls
  it and propagates the error (not just the isolated function).
- `ut-docs/reference/coding-standards.md` — companion doc recording the
  rule and both enforcement points.

Timestamp-prefixed migration numbering (the other option raised on the
ticket) was not adopted — the guard + hard-fail combination was judged
sufficient and is the smaller change; not adopting it is a deliberate,
recorded choice, not an oversight.

## Review process

**Independent review — Opus, fresh context, isolated `git worktree`, no
part in writing the fix** (`complexity:medium` → Sonnet builds, Opus
reviews, per the `scrum-master` skill's model-routing table). Ran
`gofmt`/`go build`/`go vet`/`go test ./internal/db/...`, the new guard +
its regression test, and independently re-verified the TDD claim by
disabling `checkNoDuplicateVersions` inside the isolated worktree and
re-running the wiring test — confirmed it fails with a real error
(`expected loadMigrationsFromFS to reject two files sharing version 67,
got nil error`), and that the three *isolated* `checkNoDuplicateVersions`
tests kept passing throughout, which is precisely why the wiring test
was worth adding. Restored and re-confirmed green.

Also independently validated the bug's premise against the real applier
(stronger than the ticket itself states): `internal/db/db.go`'s
`version <= current` high-watermark check means two files sharing
version 67 don't just risk an unspecified sort order — the *first*
applied one is recorded as version 67 and the second is skipped
**forever**, regardless of file-system order.

Findings, both **Medium**, both fixed before merge:

- **Zero-padding blind spot** — the guard compared version numbers as
  text (`sort -n | uniq -d` on strings extracted by regex), so
  `67_x.sql` vs `067_y.sql` was not flagged even though
  `internal/db/migration.go`'s `strconv.Atoi` parses both to the same
  int — the Go hard-fail would still have caught it, but the guard's
  entire purpose (the earlier, PR-time signal) silently didn't fire.
  Reproduced with a planted `67_zeropad_probe.sql` alongside the real
  `067_shift_cash_reconciliation.sql`: guard passed, Go loader failed.
  Fixed: normalize with bash's `10#` base-10 prefix before comparing.
- **Regression test mutated the real, embedded migrations directory** —
  `plant()` wrote fixtures straight into `internal/db/migrations/`
  (the house pattern other guard tests already use for their own trees).
  Given the high-watermark applier above, a fixture that survived a
  missed cleanup (SIGKILL, power loss, an abort inside the trap itself)
  would get `//go:embed`ed and permanently skip every real migration
  above it on any install built from that tree — the exact data-loss
  class this ticket exists to prevent, reintroduced by its own test.
  Fixed: `MIGRATIONS_DIR` is now overridable via env var, and the test
  points it at a `mktemp -d` scratch directory instead of the real tree.

Two **Low** findings accepted as follow-ups, not blocking (both already
caught by the Go loader's hard-fail — the gap is only in the guard's
earlier PR-time signal):

- A filename with no parseable leading version number was silently
  skipped by the guard (fixed anyway, alongside the two Medium findings
  above — cheap, and it now fails loud, matching the Go loader).
- The guard passed vacuously if the migrations directory itself were
  missing or renamed (fixed anyway, same reasoning).

Both low findings ended up fixed in the same pass since they were
one-line changes already touching the same file — see the commit for
the actual diff.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `go test ./internal/db/...` — full package green, including the new
  wiring tests.
- `bash scripts/ci/guard-migration-version-collision.sh` and its
  `_test.sh` — both green, including the new zero-padding,
  unparseable-filename, empty-dir, and missing-dir cases added after
  review.
- Confirmed the real `internal/db/migrations/` tree is untouched by the
  test run (`git status --short internal/db/migrations/` empty).
- `bash scripts/ci/guard-data-access.sh` green — this change adds no SQL
  query text, only Go control flow, so the repository-pattern rule is
  unaffected.
- No real client/shop name, no secret-shaped literal, anywhere in the
  diff.
- Backend/CI-only change — no `internal/pages/**` or `web/**` touched,
  so the UX-guidelines and help-topic-manual checks don't apply (noted
  explicitly, not silently skipped).

## Verdict

**Safe to merge.** Core fix correct and well-placed; both TDD claims
(the wiring test, and the guard's own regression test) independently
re-verified as real, not tautological. Both review findings were fixed
before merge; the two low-severity gaps are cosmetic (the Go loader's
hard-fail already covers them) and were fixed in the same pass since
they were trivial.

## Companion change

`ut-docs` (docs repo): `reference/coding-standards.md` gets the
corresponding rule (branch `docs/1056-migration-version-uniqueness`,
PR opened alongside this one) — reviewed as part of this same pass and
confirmed accurate against what actually shipped here.
