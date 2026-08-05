# 2026-08-05 — Accept 13-digit internal/PLU codes; stop silently dropping import data

Card: [ut-docs#293](https://github.com/universaltill/ut-docs/issues/293)
Branch: `fix/293-accept-13-digit-internal-codes`

## What shipped

1. `CatalogRepo.AddBarcode` no longer rejects an untyped 13-digit code.
   Inference labels `EAN13` only when the check digit also passes,
   otherwise `CODE128`, and **never returns an error**. Explicit
   `BarcodeType: "EAN13"` still validates and still rejects.
2. The import commit loop accumulates per-row warnings instead of
   discarding the reason and `continue`-ing past the stock step.
3. Warned rows are counted (`Warnings: N` in the summary), recorded in the
   audit payload, and exempted from the 200-row render cap.
4. Two further silent-loss paths converted to warnings: a batch-wide
   `EnsureStockLocation` failure, and a negative quantity in the source file.
5. `catimport` reports (without loosening) when it discards a barcode shape.

## Why the bug happened

ADR-0021 guarantees the catalog makes "no assumption about the code's
format or source". ut-docs#192 added EAN-13 check-digit validation but
applied it to *inferred* types, so any 13-character value was assumed to
be a retail EAN-13 and refused if the check digit failed. `validEAN13`
itself was correct — the defect was where it was applied.

**No ADR was written, deliberately**: this restores ADR-0021's guarantee
rather than departing from it. The ADR debt belongs to ut-docs#295, which
introduces real format assumptions.

## Verification

Driven CSV import, pre-fix binary (built from `origin/main` in a git
worktree) vs the fixed binary, same 3-row file:

| | barcodes attached | stock imported |
|---|---|---|
| pre-fix | **0 of 3** | **0 of 3** |
| fixed | 2 of 3 (the third is a genuine duplicate) | 3 of 3 |

The pre-fix run showed the bug was worse than the card described: the
`continue` after a barcode failure also skipped the stock step, so every
affected row lost its stock too, with no reason shown anywhere.

Second driven run at scale — 209 rows, warned rows deliberately placed
past the 200-row cap — rendered in a real browser and **looked at**:
`✓ Imported: 209 — Warnings: 3 — Skipped: 0`, with all three warned rows
pulled to the front and the "… 6 more" count still accurate.

Full gate: `go build`, `go vet`, `go test ./... -race` (34 packages, no
failures), `guard-i18n.sh`, `guard-data-access.sh`, and the full
Playwright e2e suite (38 passed).

## Independent review (Opus, fresh context) — no blockers

The review's most valuable finding was that the fix I had already verified
was **real but insufficient**: my driven run used a 3-row CSV, so it
proved the warning renders while missing that the render loop caps at 200
rows and that the summary counts a warned row as plain "created". On the
500-row migration this feature exists for, the operator would have seen
`Imported: 500 — Skipped: 0` with 40 barcodes silently missing. Fixed here.

Also fixed: the `locErr` branch (a batch-wide stock-location failure made
*every* row lose its stock while each status read a clean "created" — the
same silent-loss bug one branch over), negative quantities dropped
silently, and two properties that survived mutation testing with the whole
suite still green (a warned row's counter classification, and
`publishStockAdjusted` firing only on stock success).

Mutation-verified as genuine, not tautological: three existing assertions
were **inverted** to match the new contract, which is exactly how a
regression gets blessed. The review constructed the subtle-wrong mutation
— reverting to bare `len(...) == 13`, which stores `EAN13` *without*
validating — and the `barcode_type` assertions caught it. Without those,
inverting the accept/reject test alone would have passed a mislabel.

Found while fixing: a new test asserted on raw locale *keys*, which passed
alone but failed under the full package because `httpx`'s translator is
process-global and other tests initialise it first — order-dependent
flakiness, fixed by loading the real bundle and asserting on real text.

## Deferred, with cards

- **#304** (`p1`) — `AddBarcode` does SELECT-then-`ON CONFLICT DO UPDATE`
  with no transaction, so two concurrent writers can both pass the check
  and the second **silently reassigns the barcode to another item**: a
  customer charged for the wrong product. Pre-existing, untouched here.
- **#302** — `normalizeBarcode` still discards 4-digit produce PLUs and
  alphanumeric codes before the repository sees them, so the parser is now
  stricter than the repository it feeds. Recommended into #295.
- **#303** — operator sees raw Go errors with internal UUIDs, the whole
  import status vocabulary is untranslated English literals, and a warned
  row is **visually identical** to a successful one (found by looking at
  the rendered page, not by asserting on it).

## Known limitation, stated not implied

The explicit-EAN13 validation branch is now unreachable from production:
no call site sets `BarcodeType`. Check-digit protection is therefore
dormant until #295 wires the symbology model. Accepted deliberately —
dormant validation beats a shop that cannot import at all — and
`ErrInvalidEAN13`, the branch and both i18n keys are retained for #295.
