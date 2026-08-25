# Code review — ut-docs#968: the .bkp importer rejects every real speedy kasse backup

- **Date**: 2026-08-25
- **Branch**: `fix/bkp-meta-crc32-checksum`
- **Card**: ut-docs#968
- **Files**: `internal/catimport/bkp.go`, `internal/catimport/bkp_test.go`,
  `internal/data/bkp_products_repo.go`

## Review status — read this first

**This was a SELF-review, not the pipeline's independent different-model review.**
Subagent spawning was disabled for this session, so the Reviewer role's core
requirement — an independent model reading the diff cold — was not met. The
findings below are my own re-reading of my own change, which is worth less. **An
independent review is still owed on this branch before merge.** Recording this
plainly rather than letting the review record imply a gate that did not run.

## What prompted it

The product owner made a Raspberry Pi 4 with a fresh install available for testing
with real pilot data. Before touching the Pi, the real backup was run through
`ParseBkp` — the first time any real speedy kasse file had ever reached this code.
It failed immediately, then failed again on a second, independent defect.

## The two defects

Both are the same root cause: **the source schema was guessed from a ticket's prose
in #511/#512 and never checked against a real file.** Both were fatal — no genuine
backup could be imported at all.

1. **`meta.inf`'s per-file `checksum` is a CRC32 (8 hex), compared against a
   SHA-256 (64 hex).** Never matched, so every real backup was rejected as corrupt.
   The real file's CRC32 (`887be5e7`) was verified independently against the
   extracted bytes and matches what `meta.inf` declares — the file was intact and
   the error blamed the customer's data.
2. **`Products` has no `ProductGroupText` column** — the category lives on
   `ProductGroups`, keyed by `ProductGroupID`. Both queries selected the missing
   column, and the `"no such column"` fallback re-ran the *same* missing column
   minus the tax columns, reporting a "no-tax fallback" error that named the wrong
   cause.

## TDD, verified rather than claimed

Five tests were written **before** the fixes and observed failing with the real
production error text:

- `TestParseBkp_RealWorldMetaShape_CRC32ChecksumAccepted` → *"checksum recorded in
  meta.inf does not match the archive contents"*
- `TestParseBkp_UnrecognisedChecksumWidthDoesNotHardFail` → same
- `TestParseBkp_RealSchema_CategoryComesFromProductGroupsJoin` → *"no such column:
  ProductGroupText"*
- `TestParseBkp_RealSchema_UnmatchedGroupStillImports` → same
- `TestParseBkp_RealWorldMetaShape_WrongCRC32Rejected` passed throughout (it must
  keep rejecting a genuinely wrong checksum, and does).

The real `.bkp` is **not committable** — it carries the pilot's licence key, device
serial and business name. Fixtures reproduce its *shape* with every identifying
value replaced, per ut-docs#511's own instruction and the `no-personal-names-in-artifacts`
rule.

## Verified against the real file, not asserted

409 rows · 23 categories via the join · dine-in VAT 19%×317, 7%×78, 0%×14 ·
takeaway VAT on all 409 · 165 `source_deleted` (exactly the `Status=3` count) ·
10 `not_sellable` (exactly the `ProductType=4` count) · 13 duplicate SKU · 4 missing
name · **217 import cleanly**. Every figure cross-checked against raw SQL run
directly against the extracted database, not taken from the importer's own output.

## Points I checked in my own diff

- **SQL injection** — `buildBkpProductsQuery` concatenates only hard-coded column
  literals, gated on presence; nothing from the file reaches the query text. The
  table name in `bkpTableColumns` is a bound parameter to `pragma_table_info(?)`.
- **CRC32 is not a security control** and is documented as such on `bkpDigests`. It
  is a transfer-integrity check, which is what the vendor's field is for; archive/zip
  independently verifies its own per-entry CRC32 on every read. No security
  regression is possible here — before this change *nothing* could pass.
- **Unknown checksum widths skip rather than fail.** This is the actual lesson of
  the bug: turning "we don't recognise this" into "your backup is corrupt" is what
  blocked every import. MD5/SHA-1 widths are deliberately not guessed at.
- **`LEFT JOIN`, never `INNER`** — a product pointing at a deleted group still
  imports, with an empty category. Covered by a test.
- **`ORDER BY p.rowid`** is preserved (ut-docs#511's determinism finding). Unchanged
  risk: a `WITHOUT ROWID` Products table would break it, as it would have before.

## Suite state

`go vet` clean. `internal/catimport` and `internal/data` pass. Full `go test ./...`
has **one failure, `TestUnusualSales` in `internal/alerts`, which is pre-existing on
`main`** — verified by stashing this branch, checking out `main` and reproducing it
identically. It is date-dependent and unrelated to this diff. Carded separately;
main being red is its own problem, not this branch's.

## Not done here

- Importing `DepositPrice` (Pfand) — the real schema carries it and we ignore it.
  Belongs with #47/#249.
- The Pi 4 end-to-end import — blocked on the till's PIN, which I do not have.
