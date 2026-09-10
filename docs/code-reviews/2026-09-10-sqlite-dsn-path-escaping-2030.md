# 2026-09-10 — SQLite `file:` DSN path escaping (ut-docs#2030)

## What shipped

`internal/db.Open()` and `internal/db.OpenReadOnly()` built the SQLite DSN
via `fmt.Sprintf("file:%s?...", path)` with the raw filesystem path
interpolated unescaped. SQLite's own URI-mode filename parsing (used here
via `SQLITE_OPEN_URI`) treats an unescaped `#` or `?` in the path as ending
the path component, so a data directory containing one of those characters
silently opened a *different, wrong* file and dropped every `_pragma`
query param that followed it — including `foreign_keys(1)`,
`busy_timeout(5000)`, `journal_mode(WAL)`, `temp_store(2)` and
`_txlock=immediate` (ut-docs#311) — with no error at all.

Verified experimentally before writing the fix:
`file:/tmp/hashtest#123/db.sqlite` actually opened `/tmp/hashtest`, a
1-directory-shorter path, with the intended nested file never created.

### Fix

A new `sqliteURIPathEscaper` (`internal/db/db.go`) percent-encodes `#`,
`?` and the escape character `%` itself before the path is interpolated
into either DSN — applied in both `Open()` and `OpenReadOnly()`.
`strings.Replacer` performs a single pass over the input without
rescanning its own output, so it cannot double-encode a `%` it introduces.

`internal/db/join_snapshot.go`'s `RedactedJoinSnapshot` had the identical
unescaped DSN pattern for its own dedicated connection (found during
independent review, not in the original card). Its `copyPath` inherits the
data dir verbatim via `BackupDir`, so it carried the same exposure — with
a more serious consequence than a wrong file: a truncated DSN there
silently drops `secure_delete(1)`, the pragma ut-docs#426 relies on to
keep a redacted `bearer_hash` from staying physically recoverable in the
copy's page slack. Fixed the same way.

## Independent review

Fresh-context Opus subagent, isolated worktree (`complexity:medium` →
Opus review per model-routing rules). **Initial verdict: NOT SAFE TO
MERGE — 2 blocking findings**, both fixed in this same PR before merge:

1. **`TestOpenReadOnly_HandlesSpecialCharsInPath` was a false-pass test.**
   Its two assertions (marker row visible; `busy_timeout == 5000`) cannot
   detect the bug: under the pre-fix code, `Open()` and `OpenReadOnly()`
   truncate to the *same* wrong file, so the row is self-consistently
   visible there too, and `busy_timeout` is parsed by the Go driver
   itself at the first `?` in the whole DSN — independent of SQLite's own
   URI path truncation — so it still applies even when the path was
   truncated. The reviewer confirmed this live with `PRAGMA
   database_list` under the bug (`file="/base/hash"` vs. the intended
   `/base/hash#test/q?mark/unitill-pos.db`).

   Fixed: the test now asserts `PRAGMA database_list`'s `file` column —
   what SQLite itself believes it opened — via `os.SameFile` against the
   real intended path (not string equality, since SQLite may report a
   differently formatted but equivalent path).

2. **Missed call site in the same package**: `internal/db/join_snapshot.go`
   (described above). Reviewer proved it broken with a live repro (`no
   such table: tills` against the redacted copy at a `#`/`?` data dir) and
   flagged the `secure_delete(1)` loss as security-relevant given that
   pragma's own history (ut-docs#426: "3/4 seeded hashes survived in the
   raw bytes" without it). Fixed, with a new regression test
   (`TestRedactedJoinSnapshot_HandlesSpecialCharsInDataDir`) proving both
   the right file is used and `secure_delete(1)` actually applies (no raw
   `bearer_hash` bytes recoverable in the copy).

Also found and fixed: two non-blocking nits — an inaccurate doc-comment
claim about exactly which layer (SQLite vs. the Go driver) a bare `%`
breaks, and rebuilding the `strings.Replacer` on every call (hoisted to a
package-level `sqliteURIPathEscaper` var).

Other DSN sites the reviewer swept and found, **left as a follow-up**
(lower exposure, filed separately, not blocking this fix):
`internal/catimport/bkp.go:250` (temp-file path, `os.CreateTemp`-derived,
TMPDIR-dependent exposure) and `cmd/unitill-uninstall/verify.go:30`
(fails closed on the unfixed bug — aborts uninstall rather than deleting
data — so wrong but not dangerous; the escaping helper isn't exported so
this needs either an exported helper or a local copy). Filed as
ut-docs#2033.

## What was verified beyond automated tests

- The bug itself was reproduced experimentally (a standalone Go program
  against the real `modernc.org/sqlite` driver) before writing the fix,
  confirming the exact truncation mechanism, not just trusting the card's
  description.
- Every regression test in this PR was **personally TDD re-verified**
  (not taken on trust): the fix was reverted, each new test confirmed to
  FAIL with an error consistent with the bug (`invalid URL escape "%di"`,
  `stat ...: no such file or directory`, `no such table: tills`), then
  the fix was restored and each test confirmed to PASS again. This was
  done twice for the `join_snapshot.go` fix specifically, since the first
  revert attempt exposed that the test's own `countTills` helper (a
  pre-existing, unrelated helper reused by many other tests) has the
  identical bug for a *different* reason — a plain, non-`file:`-prefixed
  `sql.Open` call, which the Go driver truncates at the first unescaped
  `?` even without URI mode. Fixed by adding a sibling helper
  (`countTillsViaEscapedURI`) that opens through the same
  `file:`+`escapeSQLiteURIPath(...)` form production code now uses, so
  the test fails only for the production bug, not its own tooling.
- The independent reviewer additionally probed the escaping helper
  adversarially (16 inputs: multiple special chars per path, a `%`
  immediately followed by two hex digits, an already-`%23`-looking
  substring, non-ASCII, a bare `":memory:"`, an empty string) and
  confirmed: byte-identical no-op on every normal path; single-pass
  behaviour (no double-encoding); and end-to-end correct file resolution
  for 11 adversarial directory names via `PRAGMA database_list`.
- `gofmt -l .` (no output), `go vet ./...`, `go build ./...`, full
  `go test ./...` (55 packages, 0 failures — both before and after this
  PR's fixes), `golangci-lint run ./...` (0 issues),
  `scripts/ci/guard-data-access.sh` (change confined to `internal/db`, an
  allowed location for raw SQL/DB code).

## Safe-to-merge verdict

**Yes**, after both blocking findings above were fixed and re-verified.
No money, i18n, UI, help-topic, compliance-wording, plugin-signing,
kiosk-engine or Android surface touched, so no other `CLAUDE.md` guard
applies.

## Explicitly deferred

- `internal/catimport/bkp.go` and `cmd/unitill-uninstall/verify.go`'s own
  unescaped DSN call sites — tracked as ut-docs#2033, lower exposure than
  the two fixed here, not blocking this fix.
- The pre-existing nits inherited by `join_snapshot.go`'s neighbouring
  code were not touched — this PR's scope stayed to the DSN-escaping bug
  and the two call sites it actually affects.
