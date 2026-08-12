# Code review: `.bkp` import — stream `backup.db`, raise the size cap

**Card:** universaltill/ut-docs#594 (bug — `.bkp` import fails on a real
pilot backup: the 200MB entry cap is below production size, and the DB is
fully buffered in memory)
**Branch:** `feat/590-setup-wizard-detect-locale-country` (also carries the
unrelated, separately-reviewed ut-docs#590 change — out of scope here)
**Date:** 2026-08-12
**Reviewer:** independent (did not write the implementation), scope limited
to `internal/catimport/bkp.go` and `internal/catimport/bkp_test.go`
**Dev commit reviewed:** `5ee5dbd`

## What shipped (Dev)

`internal/catimport.ParseBkp` handles the speedy kasse / pepperm cashbox
`.bkp` upload (a ZIP of `backup.db` + `meta.inf`). Two defects, both found
against a real pilot café backup (270MB `backup.db`, 68,838 receipts over
22 months):

- **Cap too low.** `bkpMaxEntrySize = 200 << 20` was checked against the
  archive's declared `UncompressedSize64`, so the one real input the
  importer will ever be given was rejected outright.
- **Fully buffered.** `backup.db` went through `readZipEntry` →
  `io.ReadAll` into a `[]byte` before being written to a temp file —
  hundreds of MB of peak heap on the target iMin I22T01 (Unisoc, Android
  11) till.

The fix:

- `bkpMaxEntrySize` split into `bkpMaxMetaSize` (200MB const, still a
  declared-size gate — `meta.inf` is always fully buffered) and
  `bkpMaxDBSize` (1GB, a `var` so tests can lower it).
- `backup.db` is now streamed: `io.Copy(io.MultiWriter(tmp, hasher),
  io.LimitReader(rc, bkpMaxDBSize+1))`, with the cap enforced on
  `written`, not on the declared size.
- `validateBkpMeta(metaBytes, dbBytes []byte)` →
  `validateBkpMeta(metaBytes []byte, dbSHA256Hex string)`, fed the hash
  computed during the streaming copy instead of hashing a buffered slice.
- Tests: `TestParseBkp_BackupDBOverCapRejected`,
  `TestParseBkp_BackupDBUnderCapImportsFine`,
  `TestParseBkp_MismatchedDeclaredSizeRejected`,
  `TestBkpMaxDBSizeIsRaisedPastOriginalCap`, plus a `CreateRaw`-based
  fixture builder for corrupt-header archives.

## The load-bearing `archive/zip` claim — verdict: **TRUE**

The implementation's central design comment claims Go's `archive/zip`
refuses to let a genuinely mismatched declared-vs-actual
`UncompressedSize64` be read at all, and that this is why no declared-size
pre-check for `backup.db` is needed. This is the premise the whole
cap-enforcement design rests on, so it was verified two ways rather than
taken on trust.

**Source reading** — `$(go env GOROOT)/src/archive/zip/reader.go`
(go1.25.0), `checksumReader.Read`:

```go
n, err = r.rc.Read(b)
r.hash.Write(b[:n])
r.nread += uint64(n)
if r.nread > r.f.UncompressedSize64 {
    return 0, ErrFormat            // under-declared: caught mid-read
}
if err == io.EOF {
    if r.nread != r.f.UncompressedSize64 {
        return 0, io.ErrUnexpectedEOF   // over-declared: caught at EOF
    }
    ...
    if r.f.CRC32 != 0 && r.hash.Sum32() != r.f.CRC32 {
        err = ErrChecksum
    }
}
```

Both mismatch directions are refused, and both `return 0, …` — the bytes of
the failing `Read` are discarded, not handed to the caller.

**Empirical confirmation** — a throwaway program built mismatched entries
with `zip.Writer.CreateRaw` and read them back:

| entry (real content = 1000 bytes)  | declared      | bytes delivered | error                     |
| ---------------------------------- | ------------- | --------------- | ------------------------- |
| truthful                           | 1000          | 1000            | `<nil>`                   |
| under-declared                     | 10            | 0               | `zip: not a valid zip file` (`ErrFormat`) |
| under-declared by one              | 999           | 0               | `zip: not a valid zip file` |
| over-declared                      | 5368709120    | 0               | `unexpected EOF`          |
| over-declared by one               | 1001          | 0               | `unexpected EOF`          |
| truthful size, wrong CRC32         | 1000          | 1000            | `zip: checksum error`     |
| truthful size, **CRC32 = 0**       | 1000          | 1000            | `<nil>` (CRC not checked) |
| partial read (LimitReader) of a wrong-CRC entry | 1000 | 500        | `<nil>` — CRC never checked |

So: **no gap in the implementation.** Nothing can be smuggled through an
entry that the header doesn't declare, in either direction, so enforcing
solely on the streamed byte count rejects exactly the same set of archives
a declared-size pre-check would.

Two secondary facts fell out of the same check and are worth recording:

1. An entry whose header records `CRC32 == 0` is never CRC-verified by
   `archive/zip` at all. Pre-existing, unchanged by this diff, applies
   equally to the old `readZipEntry` path — noted, not a finding here.
2. A truncated (LimitReader-cut) read never reaches the entry's EOF, so
   the CRC is never verified in that case — exactly as the diff's comment
   says, and inconsequential because the truncated case is the rejected
   one.

But the *corollary* is what produced the one real finding below: because
declared size is trustworthy once an entry reads cleanly, a declared-size
pre-check is **sound** — it can never wrongly reject a valid archive.

## Findings

### F1 (MEDIUM, in scope) — the zip-bomb guard lost its cheap reject. **Fixed.**

The 200MB declared-size check being removed outright is a real regression
of that guard's own stated purpose (a zip-bomb guard, added by review
finding ut-docs#511). A crafted upload of a few MB — compressed
zeros, *truthfully* declaring gigabytes, so `archive/zip` reads it happily
— now gets `bkpMaxDBSize+1` bytes (**1GB**) actually inflated and written
to the till's eMMC, plus the DEFLATE CPU to produce them, before `written >
bkpMaxDBSize` notices. The old code rejected that for free, before opening
the entry. On the exact low-power hardware this card exists for, that is
tens of seconds of work and a gigabyte of temp writes for an upload that
was never going to be accepted.

Fixed by restoring the declared-size check for `backup.db` as a *fast
reject in front of* the streamed check, not in place of it:

```go
if dbFile.UncompressedSize64 > uint64(bkpMaxDBSize) {
    return Result{}, ErrBkpTooLarge
}
```

Sound for exactly the reason established above — a valid archive's declared
size is its real size, so no legitimate backup is ever wrongly rejected;
and anything with a lying header fails to read at all regardless. The
streamed `written > bkpMaxDBSize` check is deliberately kept as the
authoritative bound (it holds on the bytes themselves rather than on a
header field), with a comment recording that it is now a backstop the fast
reject pre-empts — see the mutation-test note below so nobody later reads
its lack of independent coverage as an oversight.

Measured effect, counting bytes actually pulled off the archive to reject
one over-cap entry (2MB incompressible body, cap lowered to 64KB):
**1,155 bytes with the fast reject, 99,644 without** — i.e. without it the
importer inflates the full cap's worth of body first.

New regression test `TestParseBkp_OversizedEntryRejectedWithoutStreamingIt`
pins this via a counting `io.ReaderAt`.

### F2 (MEDIUM, in scope) — the CRC32 integrity guarantee was moved but never tested. **Fixed.**

`backup.db` no longer goes through `readZipEntry`, so the *only* thing
still triggering `archive/zip`'s CRC32 verification for it is the streamed
`io.Copy` reaching the entry's real EOF. The diff correctly reasons that
`bkpMaxDBSize+1` preserves this (verified below — it does), and both the
old and new doc comments lean on CRC32 as "the baseline archive integrity
guarantee" when `meta.inf` carries no checksum — which is the common case,
since the real `meta.inf` shape is still unverified. Yet no test in the
package has ever exercised a corrupt-CRC `backup.db`: the guarantee the
comments rest on was entirely unproven, and silently importing a corrupt
till backup is the one outcome this importer must never produce.

Added `TestParseBkp_CorruptBackupDBCRCRejected` — a `CreateRaw` entry with
a truthful declared size and a deliberately wrong recorded CRC32, asserting
`errors.Is(err, zip.ErrChecksum)`. Mutation-verified (below) as
discriminating: with the copy error swallowed, the corrupt backup imports
**with no error at all**.

Required generalizing the Dev's `buildBkpZipWithMismatchedBackupDBSize`
into `buildBkpZipRawBackupDB(t, dbBytes, declaredSize, declaredCRC32,
metaBytes)`; the original helper is kept as a one-line wrapper so the Dev's
own test reads unchanged.

### F3 (LOW, in scope) — a Dev test case became a no-op under F1's fix. **Fixed.**

`TestParseBkp_MismatchedDeclaredSizeRejected`'s "declares far more than the
real content" case claimed `5 << 30` (5GB). With F1's fast reject in place
that claim is over `bkpMaxDBSize` and short-circuits, so the subtest would
still pass while no longer exercising the `archive/zip` behavior it exists
to document. Lowered to `64 << 20` (comfortably over the fixture, safely
under the cap) with a comment saying why the value must stay under the cap.

### Checked and clean — no finding

- **Memory correctness (the card's actual point).** Traced the success
  path: `meta.inf` → `io.ReadAll` (small, gated at 200MB declared);
  `backup.db` → `io.Copy` with a 32KB buffer into `io.MultiWriter(tmp,
  hasher)`; then `sql.Open` on the temp *path*. Nothing holds the DB
  bytes. Measured `runtime.MemStats.TotalAlloc` delta across a full
  `ParseBkp` call: **1MB entry = 91,304 bytes allocated; 64MB entry =
  90,768 bytes** — flat under a 64× payload increase, where the old
  `io.ReadAll` path would have allocated ≥64MB. The fix does what the
  ticket asked.
- **Cap boundary — no off-by-one, in either direction.** `io.LimitReader(rc,
  cap+1)` with `written > cap`: an entry of exactly `cap` bytes leaves the
  limiter with 1 byte of headroom, so `io.Copy` issues one more `Read`,
  reaches the entry's real EOF and gets full CRC32 + declared-size
  verification, then `written == cap` is accepted. An entry of `cap+1` or
  more stops at `cap+1`, `written > cap`, rejected. The `+1` is load-bearing:
  with a bare `cap` limit the limiter returns EOF *without* a final `Read`,
  so a `cap+1`-byte entry would be silently truncated to `cap` and
  **accepted** as a complete DB. Correct as written.
- **Checksum correctness.** The streamed hash covers exactly the bytes
  `io.Copy` writes. In the accept path that is the entire entry (the
  limiter never truncates below `cap`), so it is byte-identical to the old
  `sha256.Sum256(dbBytes)`. In the reject path the truncated hash is never
  used — `written > bkpMaxDBSize` returns before `validateBkpMeta`.
  Independently corroborated by the Dev's untouched
  `TestParseBkp_ChecksumMatchPasses`, which computes `sha256Hex(dbBytes)`
  in the test and requires production's streamed hash to equal it — that
  test is exactly the cross-check for the signature change, and it still
  tests what it claims.
- **Resource/fd handling.** `rc` and `tmp` are both closed on every path:
  `rc.Close()`/`tmp.Close()` are called unconditionally right after the
  copy, before any of the three error returns; on the `dbFile.Open()`
  failure path `tmp` is closed explicitly; `defer os.Remove(tmpPath)`
  covers every return. No leak. (Close errors are checked before the size
  check, so a hypothetical `rc.Close()` error would mask `ErrBkpTooLarge`
  — `flate`'s `Close` doesn't error in practice, and after F1 the
  oversized case returns before the copy anyway. Noted, not changed.)
- **Existing tests unaffected by the `validateBkpMeta` signature change.**
  All of the missing-file, invalid-JSON, checksum-mismatch and
  checksum-match tests re-read and re-run; each still drives the real
  public `ParseBkp` and asserts the same sentinel it always did.
- **Test quality.** All four Dev tests go through the public `ParseBkp`
  with real fixtures — no stubs, no false-pass placeholders. The
  exact-boundary test (`withBkpMaxDBSize(t, int64(len(dbBytes)))`) is
  meaningful, not accidental: it is the only test that would catch the
  cap being enforced as `>=`. `bkpMaxDBSize` becoming a mutable package
  `var` is safe here — no test in `internal/catimport` calls `t.Parallel()`
  (checked), and every mutation goes through `withBkpMaxDBSize`'s
  `t.Cleanup` restore.
- **Scope / ticket coverage.** Every item of the ticket's suggested fix
  landed: stream via `io.Copy`+`io.LimitedReader`, enforce on written
  bytes, raise the cap, fold the checksum into the streaming copy. Nothing
  skipped, no scope creep — the diff touches only the two in-scope files.
  No `internal/data` or SQL change (data-access guard green). No
  user-facing string added or altered: `internal/pages/import_page.go`
  still maps `ErrBkpTooLarge` onto the existing translated
  `import.error.bkp_unrecognised`, so no i18n or `web/help/` work is owed.
  Grepped `web/help/` and `web/locales/` — nothing anywhere quotes a
  200MB (or any) import size limit, so the cap raise leaves no stale
  documentation behind. README untouched and unaffected.
- **End-to-end viability of the 270MB case.** Checked the upload path
  actually admits a file that size: `import_page.go` calls
  `r.ParseMultipartForm(20 << 20)`, which is an in-memory *threshold*, not
  a limit — larger parts spill to a temp file whose `multipart.File` is an
  `*os.File` and satisfies the `io.ReaderAt` `ParseBkp` needs. No
  `http.MaxBytesReader` in the path. So the raised cap is genuinely
  reachable in production, not defeated one layer up.

## Verified personally

Run on the working tree after the fixes, not taken from the Dev's report:

```
$ gofmt -l internal/catimport/            → (no output)
$ go vet ./internal/catimport/...          → vet OK
$ go build ./...                           → build OK
$ go test ./internal/catimport/...         → ok  …/internal/catimport  0.137s
$ go test ./internal/pages/... -run Bkp    → ok  …/internal/pages      0.298s
    TestImport_AutoDetectsBkpUpload_Preview            PASS
    TestImport_AutoDetectsBkpUpload_Commit             PASS
    TestImport_BkpUpload_InvalidBackupShowsGenericMessage  PASS
    TestImport_BkpUploadCarriesTaxColumnsThrough       PASS
$ go test ./internal/pages/... -run 'Import.*Bkp'      → ok (3 tests)
$ go test ./...                            → all packages ok, exit 0
$ bash scripts/ci/guard-data-access.sh
    ✓ data-access guard: no inline SQL outside internal/data / internal/db
$ bash scripts/ci/guard-kiosk-engine.sh
    ✓ kiosk-engine guard: no self-order route handler references the cashier's Engine
$ bash scripts/ci/guard-plugin-menu-read.sh
    ✓ plugin-menu-read guard: no unlocked read of Pm.Installed / Pm.MenuPlugins / Menu
```

The 25 `ParseBkp` tests (including the three added/changed here) all pass.

### Mutation tests — proving the tests aren't false passes

Every one applied to the real source, run, then reverted:

| # | Mutation | Expected | Result |
| - | -------- | -------- | ------ |
| A | declared-size fast reject disabled | `OversizedEntryRejectedWithoutStreamingIt` fails | **FAIL** — "read 99644 bytes off the archive … it inflated the entry up to the cap" |
| B | `LimitReader(rc, cap+1)` → `LimitReader(rc, cap)` | some test fails | pass — see note |
| C | `LimitReader(rc, cap+1)` → `LimitReader(rc, declared size)` | CRC test fails | pass — see note |
| D | `if copyErr != nil` short-circuited to false | CRC test fails | **FAIL** — "err = `<nil>`, want zip.ErrChecksum" |
| E | `bkpMaxDBSize` reverted to `200 << 20` | raised-cap guard fails | **FAIL** — "must be well above the real pilot backup" |
| F | streamed `written > bkpMaxDBSize` check disabled | (probe) | pass — expected, documented |

Notes on the three that survived, none of which is a coverage hole:

- **A caught a false pass in my own first draft.** The counting-reader test
  originally allowed 256KB, on the wrong assumption that a streaming
  rejection would read the whole 2MB body — it reads only up to the cap
  (~97KB). Threshold re-derived from the measured 1,155 vs 99,644 figures
  and set to 16KB; the mutation then failed as it should. Recorded because
  it is exactly the class of thing this review exists to catch, including
  in the reviewer's own work.
- **B and C survive for a stdlib reason, not a test-quality one.** `flate`'s
  decompressor returns its final chunk as `(n, io.EOF)` together, so
  `checksumReader`'s EOF branch — and therefore the CRC check — still fires
  even when the limiter is set to exactly the entry size. The `+1` remains
  correct and necessary for the *acceptance* boundary (see above); it just
  isn't what the CRC path depends on for these fixture sizes.
- **F is the honest consequence of F1's fix.** With the declared-size fast
  reject in place, the streamed byte-count check is no longer independently
  reachable through the public API — `archive/zip` will not read any entry
  whose declared and actual sizes differ, so no fixture can pass the gate
  and then overrun the copy. It is kept as deliberate defense in depth, and
  the code now says so in a comment naming this mutation result, so a
  future reader doesn't mistake it for untested code and delete it.

## Fixed in this pass

Working tree, `internal/catimport/` only (nothing outside it touched):

- `bkp.go` — declared-size fast reject for `backup.db` restored (F1);
  comments on `ErrBkpTooLarge`, `bkpMaxDBSize` and the streaming block
  rewritten to describe the two-gate arrangement accurately and to record
  the verified `archive/zip` behavior with its exact error values; backstop
  comment on the streamed check.
- `bkp_test.go` — `TestParseBkp_OversizedEntryRejectedWithoutStreamingIt`
  and `countingReaderAt` added (F1);
  `TestParseBkp_CorruptBackupDBCRCRejected` added (F2);
  `buildBkpZipRawBackupDB` generalized out of
  `buildBkpZipWithMismatchedBackupDBSize` (F2); mismatched-size subtest
  lowered from 5GB to 64MB (F3).

Note on commits: the Dev's implementation was already committed as
`5ee5dbd`; the review fixes above were picked up and committed by the
orchestrator as `410efea` and `cbf6d6f` while this pass was still running.
The final backstop comment on the streamed check was left uncommitted in
the working tree, per the instruction not to commit from the review step.

## Verdict

**Safe to merge.** The implementation is correct on every point the card
cared about: the DB is genuinely never buffered (measured flat allocations
across a 64× payload), the cap boundary is right in both directions, the
checksum covers exactly the bytes it did before, and the `archive/zip`
claim the design rests on is true — verified against the stdlib source and
reproduced empirically, in both mismatch directions. Three findings, all
in scope, all fixed here: the zip-bomb guard's cheap reject restored (F1),
the CRC32 guarantee the doc comments lean on now actually tested (F2), and
one Dev subtest kept meaningful under the new gate (F3). No outstanding
findings; no UI, i18n, manual or README implication.
