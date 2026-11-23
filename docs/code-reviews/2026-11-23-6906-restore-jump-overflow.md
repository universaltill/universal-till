# Review: restore number jump ignores huge values; marker last_z uses the carried floor (ut-docs#6906)

Branch `fix/6906-restore-jump-overflow`. Built by Sonnet (Dev). Tested by a separate Opus Tester agent. Reviewed by an independent Opus 5.5 agent, a different model from the author.

## What shipped

- `data.RestoreNumberMax` (10^15) is mirrored in `internal/db` as `restoreNumberMax`, and `TestRestoreFloorKeysMirrorData` pins the two equal. A receipt or Z number above it is treated as corrupt and ignored. It is never used as a lower bound. The bound applies in these places:
  - `internal/data`: `maxReceiptSeq`, as an SQL filter on both the prefixed and the unprefixed branch, so the highest sane row still counts.
  - `internal/data`: in the jump, the stored receipt counter, the stored receipt floor and each Z floor, and the per-kind `MAX(z_number)`.
  - `internal/data`: `restoreFloor`.
  - `internal/db`: `parseRestoreFloor`, the `liveReceiptMax` queries and counter, and `liveZMax`.

  Before this change, a huge digit string saturated at MaxInt64. Adding the jump then overflowed to a negative floor, which was ignored, so numbers repeated.
- The restore marker's `LastZ` is now the floor-inclusive base, the larger of the file's highest Z and the carried floor. Before, it was the file's own highest Z. `ZFrom` and `ReceiptFrom` in the audit row keep their meaning, the file's own maxima. `LastReceipt` was already floor-inclusive.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| L1 | low | The jump's output floor can exceed the bound when an input sits just under it (corrupt `999999999999999`). Readers then ignore the reserved gap. | Deferred → ut-docs#6924 (needs a corrupt in-bound value) |
| L2 | low | `TestLiveNumberMaximaMirrorData` had no huge row, so drift between the data and db bounds went uncaught | **Fixed**: the fixture now has huge sale, archived and Z rows. Verified it fails with `liveZMax`'s bound removed. |
| F1 | follow-up | `ArchiveReport`'s `MAX(...)+1` for Z numbers has no bound. A Z near MaxInt64 overflows to REAL in SQLite. Numbers don't repeat. | Deferred → ut-docs#6924 |
| T-F1/F2/F3 | test gap (Tester) | Three bounds had no test: `restoreFloor`, the prefixed `maxReceiptSeq` filter, and the db counter plus prefixed branch | **Fixed**: three new tests, each seen failing without its bound and passing with it |
| T-F4 | pre-existing | The data-side Z floor reads have no `>= 0` check | Deferred → ut-docs#6924 |
| nit | nit | `restoreFloor` uses `fmt.Sscan`; the other parsers use `strconv.ParseInt` | Deferred → ut-docs#6924 |

The reviewer also checked how `NextReceiptNo` behaves now that `maxReceiptSeq` ignores huge rows. Before the change, a huge row seeded MaxInt64, the `+1` overflowed, `next` reset to 1, and that number hit a UNIQUE collision. Now numbering continues from the highest sane receipt. A collision would need about 10^15 sales.

## Verification

- TDD: the Tester reverted the source files and saw all four data tests and the db merge test fail with the values the dev note claims. With the source restored, they pass. I did the same revert-and-run check for the three gap tests and for the strengthened mirror test.
- `go build ./...`, `go vet` and `gofmt` are clean.
- `go test ./internal/data/ ./internal/db/` passes. `-race` on the restore tests also passes (db took 712 s with a 25 m timeout, and no race was reported). `./internal/pages -run 'Restore|Backup'` passes.
- PR CI `armv7-seccomp` caught a 32-bit build break. The untyped 10^15 const was passed to `QueryRow` as `any`, so it became a 32-bit `int` and overflowed. Fix: both consts are typed `int64`. `GOARCH=arm GOARM=7 go vet` on data and db reported 6 overflow errors before the fix and is clean after it, and `go build ./...` for arm passes.
- I ran every guard in `ci.yml` locally. All pass except four that cannot run in this container: armv7-seccomp (no qemu), catimport-version (tags not fetched), deadcode (local Go toolchain is older than go.mod) and shellcheck-version (no binary). CI runs those four.

Pre-flight: help topic n/a (internal numbering safety, no screen or step a shop owner sees); reference docs n/a (no reference page describes how the marker's `LastZ` is computed, and the floor semantics are unchanged); README n/a; en.json n/a (no strings); follow-ups deferred → ut-docs#6924.

**Verdict:** ship.
