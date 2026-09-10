# 2026-09-10 — Two remaining SQLite `file:` DSN escaping sites (ut-docs#2033)

## What shipped

Follow-up to ut-docs#2030 (`internal/db.Open()`/`OpenReadOnly()`/
`RedactedJoinSnapshot`), which found that a SQLite `file:` DSN built by
interpolating a raw filesystem path unescaped is silently mis-parsed by
SQLite's own URI-mode filename parsing on a path containing `#`, `?` or a
literal `%` — wrong file opened, trailing `_pragma` query params dropped,
no error at all. That review swept the repo for every other
`sql.Open("sqlite", fmt.Sprintf("file:...`-shaped call site and found two
more, deliberately left out of that PR's scope as lower exposure:

- `internal/catimport/bkp.go` (~line 255): `ParseBkp`'s temp copy of the
  uploaded `backup.db`, opened via `sql.Open("sqlite",
  fmt.Sprintf("file:%s?_pragma=temp_store(2)", tmpPath))`. `tmpPath` comes
  from `os.CreateTemp`, so exposure depends on `TMPDIR`'s own naming, not
  the shop's configured data directory.
- `cmd/unitill-uninstall/verify.go` (~line 50): re-opens a backup
  read-only before an uninstall deletes data. Unlike the other sites this
  one **fails closed** today — an unescaped path aborts the uninstall
  rather than silently deleting the wrong/no data — so the bug here is
  wrong behaviour (a false abort), not a dangerous one.

### Fix

Same percent-encoding pattern as ut-docs#2030 (`#`→`%23`, `?`→`%3f`,
`%`→`%25`, via a single-pass `strings.NewReplacer` so a `%` the escaper
itself introduces is never rescanned/double-encoded), applied at both call
sites:

- `internal/catimport/sqlite_dsn.go` (new file): `escapeSQLiteURIPath`,
  applied to `tmpPath` in `bkp.go`.
- `cmd/unitill-uninstall/verify.go`: same helper, local to that file,
  applied to `path`.

**Deliberately a local copy in each package, not a shared import from
`internal/db`.** `internal/db`'s own version (`escapeSQLiteURIPath`, added
by ut-docs#2030) was still unexported and sitting in an unmerged PR
(`universal-till#1046`) at the time this fix was written — and that PR's
own branch was independently active in a concurrent session's hands at
the same time, so touching `internal/db/db.go` here risked a direct
collision with live work rather than a clean merge later. `bkp.go`'s card
text itself anticipated this ("if not [already depending on
`internal/db`], a local copy... is acceptable — call this out explicitly
so it isn't mistaken for scope creep"). A follow-up should consolidate
the (now three) copies into one exported helper once `#1046` lands —
tracked as remaining scope on this card, not silently dropped.

## Independent review

Fresh-context Sonnet subagent (`complexity:easy` → Sonnet review per
model-routing rules), same shared checkout (not an isolated worktree —
noted as a process gap below, not a defect in the review itself).

**Verdict: SAFE TO MERGE — one blocking gap found and fixed.**

### Finding (blocking, fixed)

The original `internal/catimport` regression tests
(`sqlite_dsn_test.go`'s `TestEscapeSQLiteURIPath_ResolvesToIntendedFile`/
`_NoOpOnNormalPath`) only exercised the `escapeSQLiteURIPath` helper in
isolation against a DSN they built themselves — they never called
`ParseBkp`, so they would keep passing even if `bkp.go`'s own call site
were reverted to the unescaped form. The reviewer proved this empirically
(reverted the call site, full `internal/catimport` suite still green),
then added `internal/catimport/bkp_test.go::
TestParseBkp_TMPDIRWithSpecialCharsStillImports`, which points `TMPDIR`
at a directory containing `#`/`?`/`%` and drives `ParseBkp` end-to-end.
One subtlety the reviewer caught and fixed inline: the test's own fixture
builder (`buildBkpDBBytes`) opens its own scratch file via a *bare*
(non-`file:`-prefixed) DSN, which `modernc.org/sqlite`'s DSN parsing
*also* splits on the first unescaped `?` — an unrelated, pre-existing
characteristic of bare-path opens, not this card's bug. Building the
fixture bytes **before** `t.Setenv("TMPDIR", ...)` avoids that
interaction so the test isolates the actual call site under test.
Verified: fails against the pre-fix call site (`invalid URL escape
"%di"`), passes against the fix.

`cmd/unitill-uninstall`'s `TestVerifyBackupGoodAtSpecialCharPath` already
called the real `verifyBackup(path)` function directly, so that side was
correctly covered from the start (reverting `verify.go`'s call site makes
it fail as expected).

### Things independently confirmed correct

- **No double-encoding**: `strings.NewReplacer` performs one simultaneous
  pass over the *original* string, so a `%` the escaper introduces (from
  escaping `#`/`?`) is never rescanned. Verified against a directory
  literally named `literal%23dir` — round-trips correctly.
- **Both real call sites fixed, no others missed** — grepped the whole
  repo for `"file:` DSN construction outside `internal/db` (out of
  #2030's scope); only these two exist. Other `sql.Open("sqlite", path)`
  calls in these packages are bare-path test fixtures, not `file:` URIs.
- **Edge cases**: empty path (escapes to `""` harmlessly, unreachable at
  either real call site — both preceded by existence checks), a literal
  `%23`-looking substring, and non-ASCII (`café-商店-δοκιμή`) all verified
  via a scratch program to round-trip/resolve correctly (`os.SameFile`
  true).
- No money/i18n/kiosk-engine/compliance-wording surface touched
  (`guard-kiosk-engine.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`
  all pass) — pure backend DSN-construction fix.

### Nit, noted not fixed

`modernc.org/sqlite`'s bare-path DSN parsing (no `file:` prefix) also
splits on the first unescaped `?`, independent of `escapeSQLiteURIPath` —
so any bare-path `sql.Open("sqlite", path)` elsewhere in the codebase
(e.g. `bkp_test.go`'s own `buildBkpDBBytes` helper) is latently exposed
to a literal `?` in a path, for test fixtures only as far as this review
found. Worth a future ticket; out of ut-docs#2033's stated two-site scope.

### Process note

The review subagent ran against the same shared working tree rather than
an isolated worktree, and a stop-hook-forced commit landed mid-review
(the subagent never ran `git commit`/`git checkout` itself — see
`SKILL.md`'s `BUILD-CYCLE.md` step 4 note on this exact risk,
ut-docs#386). No harm resulted here: the committed content matches
exactly what the reviewer went on to review and fix, and the working
tree was re-verified clean and green (full `go test ./...`, `golangci-lint`)
both before and after. Flagged for visibility, not as a defect in this
diff.

## What was verified beyond automated tests

- Every new regression test personally TDD re-verified: the fix reverted,
  each new/modified test confirmed to FAIL with an error consistent with
  the bug (`invalid URL escape "%di"`), then the fix restored and each
  test confirmed to PASS again.
- `gofmt -l .` (no output), `go vet ./...` (clean), `go build ./...`
  (clean), full `go test ./...` (0 failures across every package),
  `golangci-lint run ./...` (0 issues), `scripts/ci/guard-data-access.sh`
  (pass — these DSN strings aren't domain SQL query text, correctly
  outside the guard's scope).

## Safe-to-merge verdict

**Yes.** Both call sites named in the card are fixed with the same
escaping pattern as ut-docs#2030, proven by regression tests that
actually exercise the real call sites (not just the helper in isolation),
and the full gate is green. No architectural decision or ADR needed —
mechanical bug fix, same shape as ut-docs#2030.

## Explicitly deferred

- Consolidating the (now three) local copies of the same escaping helper
  into one exported `internal/db` helper, once ut-docs#2030's PR
  (`universal-till#1046`) merges.
- The bare-path DSN `?`-splitting characteristic noted above (test
  fixtures only, as far as this review found) — a future ticket, not
  this card's scope.
