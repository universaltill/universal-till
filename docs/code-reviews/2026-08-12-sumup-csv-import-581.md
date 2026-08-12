# Code review: SumUp items-export CSV import (German café shadow pilot)

**Card:** universaltill/ut-docs#581 (p1, complexity: medium, pilot-blocking)
**Branch:** `pipeline/581-sumup-csv-import`
**Date:** 2026-08-12
**Complexity:** medium — Dev inline (Sonnet), Review via an independent Opus
subagent (fresh context, isolated worktree). One review round; the round
found no blocking issue and two small in-scope fixes, folded into this same
diff rather than earning a second round. No money/tax/data-loss/security-
class blocker on the diff itself, though the review surfaced a real
pre-existing risk outside this diff's scope — escalated separately below.

## What shipped

`universal-till` can now import a SumUp items-export CSV — the catalog
source for the German café shadow pilot (their menu lives in SumUp today).
Extends the existing interim core importer (`internal/catimport`, the same
pragmatic route #511 took for speedy-kasse) rather than the plugin-based
route (#522/#524), which isn't built yet.

- `internal/catimport/catimport.go`:
  - `DetectFormat`: new `"sumup"` case, matched on the phrase `"set up
    different prices and vat for takeaway"` (SumUp's takeaway-VAT-toggle
    column name, effectively unique to SumUp) — checked ahead of the
    `generic-erp`/`generic` fallback. A second, narrower fallback signature
    (`"item id"` + `"variant id"`, both matched as **exact** per-header
    values via a new `hasExactHeader` helper) covers an export with that
    toggle column omitted, and is checked *after* the `department` check so
    a genuine ERP-master export always wins first.
  - `columnSynonyms["tax"]`: added the literal `"tax rate (%)"` — SumUp's
    real header text, which didn't match the pre-existing `"tax rate"`
    exact-match/`"tax rate ["`-prefix rule.
  - `Result.Format` doc comment updated to list `sumup`.
- `internal/catimport/catimport_test.go`: `TestParseSumUp` (3-row synthetic
  fixture — umlaut category names, a (19.00,7.00) dine-in/takeaway override
  case mirroring #512's bug class, a barcode, decimal prices),
  `TestDetectFormatSumUpFallbackSignature`, and
  `TestDetectFormatGenericERPNotStolenBySumUpFallback` (the review's own
  regression test for finding M1, below).

**Nothing else needed changing.** `internal/pages/import_page.go`'s
preview/commit path (tax-code creation, takeaway-override merge into
`ut-plugin-tax-de`'s setting, `BarcodeExists`/`SKUExists` dedup) is entirely
format-agnostic — it branches on `ImportItem` fields
(`HasTax`/`TaxRateBP`/`HasTakeaway`/`TakeawayRateBP`/`Barcode`/`SKU`), never
on `Result.Format` except to display/log it. Verified by reading the handler
in full, not assumed.

## Independent review (Opus, fresh context, isolated worktree)

Read the full diff, read `import_page.go`'s commit path itself (didn't take
the "format-agnostic" claim on faith), ran the full gate, and independently
re-verified the TDD claim.

### Findings

**M1 — non-blocking, FIXED in this diff. `DetectFormat` false positives
against `generic-erp`.** The original fallback signature used
`strings.Contains` on the joined, lowercased header string for `"item id"`
and `"variant id"`, which substring-matched inside unrelated real headers —
probed and confirmed:
```
Item Name,Item Identifier,Variant Identifier,Price,Department   -> sumup (wrong, want generic-erp)
Item Name,Parent Item Id,Child Variant Ids,Price,Department     -> sumup (wrong, want generic-erp)
```
Blast radius was cosmetic even before the fix — `Format` never drives
parsing behaviour (only the square variation-append, and square is matched
earlier in the switch) — but the preview banner and audit log would have
shown the wrong source label for a real ERP export carrying those column
names. Fixed by (a) moving the `department` check ahead of the sumup
fallback so a genuine ERP master always wins, and (b) replacing the
substring `Contains` fallback with a new `hasExactHeader` helper that
requires an exact, trimmed, case-insensitive per-header match — so
`"Item Identifier"` can never satisfy `"item id"` again. Regression test:
`TestDetectFormatGenericERPNotStolenBySumUpFallback` (4 cases, including the
two exact header shapes above plus a no-department variant that must land
on plain `generic`).

**M3 — non-blocking, FIXED in this diff. Fixture didn't match the AC's own
wording.** The acceptance criteria say "19.00 / 7.00 land as the correct tax
codes" (matching real SumUp's two-decimal export), but the original fixture
used bare `19`/`7`. Changed the fixture to `19.00`/`7.00` — `ParseTaxRateBP`
handles both identically (1900/700), so this only tightens the test to
actually exercise the literal AC text and real SumUp output shape, no
behaviour change.

Nits noted, not fixed (both real, both out of this card's scope):

- **M2 — the literal `"tax rate (%)"` synonym is a minimal stopgap, not the
  durable fix.** It's brittle to header variants (`"Tax rate(%)"` with no
  space, `"VAT rate (%)"`, `"Tax (%)"` all still miss, silently — no
  `TaxIssue` fires because the *column* isn't recognised, only a
  recognised-but-unparseable *cell* sets `TaxIssue`). A general fix
  (stripping a trailing parenthesised suffix before synonym matching,
  mirroring the existing `s+" ["`-prefix rule) would cover this class in one
  place, but has wider blast radius across every synonym set, not just tax —
  reasonable to scope as its own card with its own tests. No collision
  found with the existing `"takeaway tax rate"` synonym (matching is exact/
  bracket-prefix, not substring `Contains`), so the takeaway column is safe
  as shipped.
- **M4 — idempotency for SumUp specifically is inherited, not directly
  exercised.** No test in this diff drives a SumUp file through
  preview→commit→re-commit. Given the verified format-agnosticism of
  `import_page.go` and the eight existing `TestImport_Tax*`/dedup tests
  already covering that mechanism generically, this is accepted as
  real-but-covered rather than a gap needing its own test. One caveat worth
  recording: dedup keys off `SKU`/`Barcode` only, both of which are optional
  in a SumUp export — a file with both blank would re-import as duplicates.
  Worth confirming the café's actual export populates SKU before the pilot
  import runs for real.

Not applicable, checked anyway: the two recurring bug classes this pipeline
keeps finding — a file-write handler missing `os.MkdirAll`, and a
cwd-relative path where `paths.Data(…)` belongs — genuinely don't apply.
The diff writes nothing to disk (grepped for `os.`/`filepath`/`paths.`/
`MkdirAll`/`WriteFile` — zero hits); `catimport` is a pure in-memory parser
by design (see the package doc comment).

### Escalated separately, not a merge blocker for this card

**German comma-decimal prices silently parse ~100× wrong, with no warning.**
Pre-existing behaviour in `ParsePrice`, affecting every CSV format this
importer supports, not introduced by this diff — and this card's own AC
specifies dot-decimal form, which the diff correctly satisfies. But this
card exists *for* the German café pilot, and a German SumUp account
commonly exports comma decimals. Reviewer's probe:
```
"3,50"      -> PriceMinor = 35000  (want 350;  10,000% too high, no error)
"1.234,50"  -> PriceMinor = 123    (thousands-separator misread as decimal)
```
Silent on the price side (no `Issue` set — the row imports as "ok" at a
grossly wrong price); the tax side at least raises `TaxIssue` when a comma
form breaks `ParseTaxRateBP`. Filed as a new, separate p1/complexity:hard
card (universaltill/ut-docs#586) rather than folded into this diff — fixing
`ParsePrice` to be locale-aware (or to reject ambiguous separators) touches
every import format, not just SumUp, and deserves its own design and test
pass. Flagged as gating the actual pilot import, not this card's merge.

## Verified beyond the automated suite (this session)

- **TDD claim independently re-verified, twice** (once by Dev while
  building, once by the independent Opus reviewer in its own isolated
  worktree — not taken on the other's word). Reverted only
  `internal/catimport/catimport.go` to its pre-fix state, re-ran
  `TestParseSumUp`/`TestDetectFormatSumUpFallbackSignature`: both failed,
  with `TaxRateBP:0 HasTax:false` while `TakeawayRateBP` still came through
  — the exact ut-docs#512 bug class (dine-in rate silently dropped, takeaway
  rate not). Restored the fix: both pass again, and a final `git diff`
  against the pre-revert commit came back empty.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/catimport/... -v` — all 27 tests pass, including the
  three SumUp-specific ones and the pre-existing bkp/loyverse/square/generic/
  generic-erp/tax-column suite (no regression).
- Full `go test ./... -count=1` — 34 packages ok, 0 FAIL, exit 0. Run twice:
  once after the initial Dev diff, once after folding in the M1/M3 fixes.
- All five guard scripts green: `guard-data-access.sh`, `guard-i18n.sh` (953
  template keys, all locales match en.json — this diff adds zero new
  user-facing strings; `"sumup"` flows through the same untranslated `%s`
  slot `"loyverse"`/`"square"` already use), `guard-help-topics.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`.
- **No manual/help-topic work is owed.** The change is backend-only: no
  route, template, or visible control added or changed. The import page's
  existing "detected: …" status line and preview/commit flow are unchanged
  in shape — only which source formats it recognises grew by one.
  `guard-help-topics.sh`'s page-route coverage passes unchanged, and
  `README.md` claims nothing this diff affects.
- **No real café/shop data anywhere in the diff.** Item names (`Cappuccino`,
  `Käsekuchen`, `Milchkaffee to go`), SKUs (`SU-001..003`) and the EAN
  (`4006381333931`, the repo's own pre-existing synthetic EAN already used
  in `genericCSV`) are all synthetic, per the card's own explicit
  requirement never to commit the café's real export.
- **No secret-shaped literal anywhere in the diff.**

## Safe-to-merge verdict

**Yes.** The fix is correctly scoped to the real, verified gap (SumUp's
`"Tax rate (%)"` header didn't match any existing synonym, and the file
would have silently fallen through to `generic`, losing the 19%/7% German
VAT split on every row). `import_page.go`'s downstream tax/takeaway/
idempotency handling is confirmed format-agnostic, so no other file needed
touching. M1's detection collision and M3's fixture-AC mismatch are both
fixed in this same diff, with a regression test proving M1's fix. M2 and M4
are legitimate, correctly-scoped follow-ups, not blockers. The comma-decimal
`ParsePrice` risk is real but pre-existing and out of this diff's scope —
escalated as ut-docs#587, gated before the pilot's real import runs rather
than blocking this card's merge.
