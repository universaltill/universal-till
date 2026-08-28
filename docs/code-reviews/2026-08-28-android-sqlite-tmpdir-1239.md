# Code review — Android first-boot death: SQLite temp dir (ut-docs#1239)

**Date:** 2026-08-28 · **Branch:** `fix/1239-android-sqlite-tmpdir` ·
**Reviewer:** independent Opus subagent (different model), findings applied
by the implementing session.

## What the change is

First-ever boot of the till on real Android hardware (Teclast P50T,
Android 16) died inside migration 036 with `disk I/O error (6410)` =
`SQLITE_IOERR_GETTEMPPATH`, leaving a bare white screen. 036 is the first
migration to use `CREATE TEMP TABLE`; with SQLite's default `temp_store` a
temp table is file-backed, and an unrooted Android app has no writable
default temp directory (TMPDIR unset; Go's `os.TempDir()` fallback
unwritable by an app uid). Every earlier emulator verification predated 036,
which is why this was never seen.

Fix, two independent layers:

1. `internal/db/db.go` — `_pragma=temp_store(2)` (MEMORY) in the DSN, every
   pooled connection. Temp tables/indices/statement sub-journals never touch
   the filesystem, on any platform.
2. `mobile/mobile.go` `Start()` — when TMPDIR is unset **or points at a
   directory that no longer exists**, create `<dataDir>/tmp` and export it.
   This protects the Go-side `os.CreateTemp` callers (CSV/catalogue import
   staging, `.bkp` import, self-update), NOT SQLite — see review note below.

Tests: `TestOpenTempStoreMemory` (PRAGMA readback; red before the change),
`TestStart_SetsWritableTMPDIRWhenUnset`, `TestStart_RespectsExistingTMPDIR`,
`TestStart_ReplacesStaleTMPDIR`, plus a TMPDIR-hygiene guard in
`mobileTestEnv`.

## Independent review findings and what happened to them

1. **BLOCKER (found by review, confirmed real): the first draft's
   `TMPDIR == ""`-only guard broke the whole `mobile` test package on any
   machine without TMPDIR** (ubuntu CI runners; macOS masks it by always
   setting TMPDIR). Start exported TMPDIR into a `t.TempDir()` that the
   test cleanup then deleted; `testing.T.TempDir()` resolves through
   `os.TempDir()`, so every later test failed before its body ran (reviewer
   reproduced: 1 pass / 6 fail under `env -u TMPDIR`). **Fixed** exactly as
   the reviewer suggested: a TMPDIR pointing at a missing directory now
   counts as unset (`isDir` check) — which also fixes the production
   staleness below — and `mobileTestEnv` clears a dangling TMPDIR then pins
   a per-test one. `env -u TMPDIR go test ./mobile` now passes 9/9.
2. **SHOULD-FIX: sticky TMPDIR across `Start(dirA)`→`Stop`→`Start(dirB)`**
   left the export pointing into a possibly-deleted dirA. **Fixed** by the
   same staleness check; covered by `TestStart_ReplacesStaleTMPDIR`.
3. **NOTE: modernc's libc snapshots the process environment on first use**
   (verified by the reviewer against `modernc.org/libc`'s source both
   directions), so a TMPDIR exported after any libc touch never reaches
   SQLite — the pragma is SQLite's actual fix, TMPDIR is for Go-side
   callers only. **Applied** as a comment rewrite so nobody later treats
   TMPDIR as SQLite's safety net and reverts the pragma.
4. **NOTE: the pragma's blast radius is wider than the ticket** — it also
   moves statement sub-journals and ephemeral b-trees (DISTINCT, unindexed
   ORDER BY/GROUP BY) off the filesystem, each of which would have been a
   *runtime* Android crash later. Accepted as-is (good news, no code
   change).
5. **NOTE: desktop memory regression risk assessed and bounded** — with
   temp_store=MEMORY the sorter never spills; reviewer checked the actual
   workloads (no bare `VACUUM` anywhere; `VACUUM INTO` doesn't drive the
   sorter for bulk; the only full-table DISTINCT is over a tiny set). New
   standing constraint noted: no unindexed sort/index build over a
   multi-hundred-MB table on any platform.
6. **NOTE: two handles outside the invariant** —
   `internal/db/join_snapshot.go` (own DSN) and `internal/catimport/bkp.go`
   (no pragmas). **Applied**: both now carry `_pragma=temp_store(2)` with a
   one-line comment, keeping "temp-dir-free" true by construction for every
   SQLite handle in the codebase.
7. **NOTE: `t.Setenv("TMPDIR", "")` semantics** — reviewed as correct and
   leak-free (restores on cleanup, LIFO before Stop). No change.

## Verification

- `go build ./...`, `gofmt -l .` clean; `go test ./internal/db
  ./internal/catimport ./mobile` green; `env -u TMPDIR go test ./mobile`
  green (the CI-shaped run that failed before finding 1 was fixed).
- **Real-device verification (Teclast P50T):** a debug APK of this branch
  installed over a till whose DB predated migration 036 → boot ran 036+ on
  device successfully, server up, `/healthz` 200, `files/tmp/` created;
  German language + Germany fiscal plugins then installed from the live
  cloud marketplace over Wi-Fi and `<html lang="de">` rendered.
- Build-tooling trap found while verifying, worth its own card: a stale
  July 27 `android/app/libs/unitill-mobile.aar` was silently packaged when
  `gomobile` wasn't on gradle's PATH — the `generateAar` failure was
  masked in this session by a `| tail` pipeline, but the aar also sat
  stale on disk for a month. Filed as a follow-up (see ut-docs#1239
  comments).
