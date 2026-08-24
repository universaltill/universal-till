# 2026-08-24 — barcode symbology parser + registry core package (ut-docs#933)

## What shipped

Foundation card of the ADR-0059 split (ut-docs#295 epic → #933/#934/#935/#936).
New leaf package `internal/barcode` in `universal-till`:

- `Symbology` (id, i18n `NameKey`, `Parse(code string) (Decoded, bool)`) and
  `Decoded` (lookup key, plus an optional embedded price (`money.Money`) or
  weight (decimal string) for the two embedded-data symbologies).
- `Registry` holding the ADR-0059 §1 default set of ten symbologies:
  `EAN13`, `EAN8`, `UPCA`, `UPCE`, `CODE128`, `CODE39`, `GTIN14` (merged
  ITF-14 + GS1 DataBar-as-GTIN-14), `INTERNAL_PLU`,
  `EAN13_WEIGHT_PREFIX2X`, `EAN13_PRICE_PREFIX02`.
- `Registry.Match(enabledIDs, code)` — the single shared matching function
  #934/#936 will both call — tries the two embedded-data entries before any
  plain entry (specificity order, ADR-0059 Decision §3), so a `20`-`29`-
  prefixed weight-embedded EAN-13 (also structurally a valid plain EAN-13)
  resolves to `EAN13_WEIGHT_PREFIX2X`, not `EAN13`.
- `barcode.ValidEAN13Checksum` extracted from `internal/data/catalog_repo.go`'s
  private `validEAN13`, which now delegates to it (ADR-0059's "reuse/extract
  rather than duplicating" instruction) — behaviour-identical, existing
  `AddBarcode`/`validEAN13` tests unchanged and still green.
- No `Registry.Register` plugin seam (ADR-0059 Decision §5 explicitly defers
  this — WASM plugins can't receive a Go closure).
- No dependency from `internal/barcode` back into `internal/data` or
  `internal/pages` (only `internal/money` + stdlib).

Out of scope for this card (tracked on the follow-on cards): wiring into
`AddBarcode`/the scan path (#934), the settings checklist UI (#935),
`internal/catimport` integration (#936).

## Independent review

One round, `general-purpose` subagent at **Opus** (`complexity:hard`
→ Opus review per the scrum-master skill's model routing), run in an
isolated worktree with full tool access (not a same-process review — it
ran `go build`/`go vet`/`go test`/the guard scripts itself, and wrote its
own independent Python GS1-checksum and UPC-E-expansion implementations to
re-derive every test vector from scratch rather than trust the shipped
algorithm).

**Verdict as delivered: one blocker, otherwise safe to merge.** All fixed
below before this record was written; this is the review of the diff as it
actually shipped, not the pre-fix draft.

### F1 — BLOCKER, fixed
`Registry.Match`'s doc comment claimed "no overlap within a tier," but the
`Default()` declaration order put the permissive catch-alls
(`CODE128`/`CODE39`) *before* `GTIN14`. Since `Match` tries same-tier
entries in declaration order, a genuine GTIN-14/ITF-14 scan resolved to
`SymbologyID: "CODE128"` whenever CODE128 was enabled (i.e. always, under
the default-enabled set) — the review's own `TestGTIN14SingleMatch`-style
check missed this because it called `Parse` directly, never `Match`.
**Fix:** reordered `Default()` so every checksum/structurally-validated
plain entry (`EAN13`/`EAN8`/`UPCA`/`UPCE`/`GTIN14`) is declared before the
three permissive catch-alls; corrected `Match`'s doc comment to say
declaration order *does* decide the outcome within the plain tier, and
why. Added `TestMatchGTIN14ResolvesWithFullDefaultSet` — Match with every
default entry enabled must resolve a valid GTIN-14 to `GTIN14`, not a
catch-all — as the regression guard.

### F2 — should-fix, documented (not a code defect)
EAN-8 and UPC-E are both 8 digits and both checksum-validated — the
reviewer exhaustively enumerated all 20,000,000 eight-digit strings
starting `0`/`1` and found 1,160,000 (58%) of valid UPC-E codes are *also*
structurally valid EAN-8. No declaration order removes this — it's a real,
irreducible ambiguity, unlike F1. `LookupKey` is identical either way (the
scanned code itself), so item resolution is unaffected; only the recorded
`barcode_type` can differ from the "true" symbology. **Fix:** documented
explicitly in `Match`'s doc comment and at the `UPCE` registry entry;
added `TestMatchEAN8UPCEOverlapIsDocumentedNotAccidental` using
`"01234565"` (a real, commonly-cited UPC-E test code that happens to also
satisfy EAN-8) so a future reordering changes this outcome deliberately,
with the test updated to match, rather than silently.

### F3 — should-fix, documented as a design note for #934
`UPCE`'s `LookupKey` is the scanned 8-digit compressed code, not its
12-digit UPC-A expansion — correct per the ADR's "raw code for plain
symbologies" wording, but means a catalog entered with the expanded UPC-A
form won't match a UPC-E scan of the same product. Added an explicit note
on `Decoded.LookupKey` flagging this for #934/#935 to decide on
deliberately, rather than it being discovered at a pilot till.

### F4 — should-fix, fixed
Only one of UPC-E's four zero-suppression branches (case marker `3`,
`validUPCE`) had test coverage; the newly-extracted `ValidEAN13Checksum`
had no direct test. Reviewer independently re-derived the other three
branches against the standard UPC-A↔UPC-E table and confirmed the shipped
`upcEToUPCA` matches. **Fix:** added `TestUPCEZeroSuppressionBranches`
(one vector per case marker: 0-2, 3, 4, 5-9, each with a corrupted-check-
digit negative case) and `TestValidEAN13Checksum` (direct test of the
exported function). Coverage: 84.8% → 93.5%.

### F5 — nit, fixed
A third hand-rolled copy of the EAN-13 checksum existed in
`internal/db/barcode_seed_test.go`'s `isValidEAN13` (test-only, different
package). Now delegates to `barcode.ValidEAN13Checksum` too, so the
ADR-0059 "reuse/extract" goal is fully met, not two-thirds met.

### F6 — nit, fixed
`strconv.Atoi` on the embedded weight/price digit substrings had a dead
error branch — both callers had already validated the input was all-digit
via `isAllDigits`, so the error path was provably unreachable. Replaced
with a small `digitsToInt` helper with no error return, so the code no
longer implies a failure mode that doesn't exist.

### F7 — nit, documented
A zero-valued embedded weight/price (`"0.000"` / `0` minor units) parses
successfully — legitimate at this layer (a scale can print a zero label),
but #934 must decide deliberately whether a zero-qty/zero-price scan may
create a basket line. Documented on `Decoded`.

### F8 — nit, not fixed (noted only)
`Match`/`IDs`/`Lookup` on a nil `*Registry` panic on field access; the
only exported constructor is `Default()`, so this is unreachable in
practice. Left as-is per the reviewer's own "mentioning only for
completeness" framing — not worth an exported-API change for an
unreachable case.

### Confirmed clean (reviewer's own independent verification, not just a read)
- **All 12 check-digit test vectors re-derived from scratch** (independent
  Python GS1 mod-10 + UPC-E expansion implementations, no reference to the
  shipped code) — every "valid"/"bad" pair matched exactly.
- **`TestSpecificityOrder` is a real regression guard, not a tautology** —
  proved by deliberately swapping `matchTier`'s embedded/plain call order
  and confirming both embedded subtests fail with the wrong `SymbologyID`,
  then restoring and confirming green again.
- **`gs1CheckDigit`'s mutation sensitivity confirmed** — flipping the
  weight-alternation start value breaks EAN13/EAN8/UPCA/UPCE/GTIN14 and
  both embedded-data tests.
- **UPC-E expansion is standards-correct** — all four branches independently
  derived from the published UPC-A↔UPC-E zero-suppression table and
  compared line-for-line against `upcEToUPCA`.
- **Digit-range math has no off-by-one** — `code[7:12]` on a 13-byte string
  is 0-indexed 7-11 = 1-indexed digits 8-12, matching the ADR; `LookupKey`
  is 7+6=13 characters, zeroing digits 8-13 (5 embedded + 1 check).
- **Weight/price prefixes are disjoint** (`code[0] != '2'` vs.
  `code[0:2] != "02"`); confirmed prefix `29` parses as weight and is
  rejected by the price parser.
- **Thread safety** — `Registry` is read-only after construction, no
  shared mutable state; 64 goroutines × 500 iterations of
  `Match`/`IDs`/`Lookup` under `-race`: clean.
- **`enabledIDs` handling** — unknown/duplicate/empty/nil ids all handled
  gracefully, no panic.
- **Money rules, leaf-package rule, i18n deferral to #935** all confirmed
  correct by direct inspection and grep (no `internal/data`/`internal/pages`
  import; no file writes/paths, so the two recurring bug classes this
  pipeline watches for are genuinely N/A here).
- **The `validEAN13` extraction is behaviour-preserving**: reviewer
  re-implemented the *deleted* left-indexed algorithm and compared it
  against `barcode.ValidEAN13Checksum` over 2,000,000 randomised strings
  (varying length, ~5% junk bytes) — zero mismatches.
- No real client/shop name, no secret-shaped literal anywhere in the diff.

## Verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./...` clean.
- `go test ./internal/barcode/...` — 12 top-level tests / 20 subtests,
  all pass; 93.5% statement coverage.
- `go test ./internal/data/... ./internal/db/...` — full package suites
  green after the `validEAN13`/`isValidEAN13` delegation changes,
  including the pre-existing `TestAddBarcode_ValidatesExplicitEAN13AndPreservesArbitraryCodes`
  and the `internal/db` seed-checksum guard tests.
- `go test ./... -race` — full repo suite green (a `TestHostTCPOpenDeniedWithoutPermission`
  timeout in `internal/plugins` — a package this diff does not touch — on
  the first full-suite run was confirmed to be resource contention from
  running the entire suite under `-race` in parallel in this sandbox, not
  a real failure: it passed in 1.83s without `-race` and in 18.7s with
  `-race` when run in isolation. Not this diff's concern.)
- `scripts/ci/guard-data-access.sh` — no inline SQL outside `internal/data`/
  `internal/db` (this package has none at all).
- All 16 CI-blocking `build`-job guard scripts run directly — all pass
  (the rest are no-ops against this diff: no UI/template/i18n/kiosk/
  Android/Makefile-version surface touched).

## Deferred / follow-on

- F3 (UPC-E/GTIN-14 catalog-entry normalisation) and F7 (zero-embedded-
  quantity handling at the basket-line layer) are explicitly flagged for
  #934, not re-solved here — they're scan-path/`AddBarcode` wiring
  decisions, out of this card's scope per ADR-0059's own card split.
- F8 (nil `*Registry` panic) noted but not fixed — unreachable via the
  only exported constructor.
