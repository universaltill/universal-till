# Code review: Day-close payment-method x VAT-rate cross-tab

- **Card:** universaltill/ut-docs#1004 (dependency spine #1003 → #1004 → #1005; #1003 merged as universal-till#524)
- **Repo:** `universal-till`
- **Dev:** independent Fable subagent (complexity:hard build tier)
- **Reviewer:** independent Opus subagent, fresh context, no shared context with the Dev subagent (complexity:hard review tier — deliberately not Fable, so the review doesn't share the builder's blind spots)

## What shipped

`EODReport.MethodTaxBands []MethodTaxBand{Method,RateBP,Net,Tax,Gross}` —
a payment-method x VAT-rate cross-tab, minor units, feeding the
DATEV/accounting export posting batch (debit account = payment method,
credit account = VAT rate). Builds on `EODReport.TaxBands` (#1003).

- `internal/data/pos_repo.go`: new `MethodTaxBand` type; `EODTaxBandSale`
  gains `Payments []EODTaxBandPayment{Method,Amount}` (`Amount = amount −
  change_given − tip_amount`, tips excluded — no VAT rate applies to a
  tip); a third fixed query added to `SalesForTaxBands` (still O(1)
  queries regardless of sale count).
- `internal/pages/eod_method_tax_bands.go` (new): per sale, apportions
  each `pos.VATBandsForSale` rate band across that sale's payments —
  floor-division per payment except the last, which takes the exact
  remainder (drift-free: the parts always sum back to the sale's own band
  amount). Gross is *derived* as `Net+Tax` per cell after aggregation,
  never apportioned as an independent third split — this is what
  guarantees the per-cell `Gross == Net+Tax` invariant by construction.
  Sign-flips returns using their own payment split.
- `internal/pages/eod_api.go`: new `rateTableRows` helper (see Findings
  #1) shared by "BY VAT RATE" and, grouped per method, "BY METHOD & VAT
  RATE" in the printed Z-report; `titleFirst` helper (Findings #5).
- `web/help/{en,tr,ar,fa}/reports.md`: new manual subsection (manual-
  ships-with-feature standing rule).

## Independent review — findings (all addressed)

1. **BLOCKER — the first-draft dedicated 5-column print layout silently
   clipped digits at ordinary amounts.** A single cell like
   `{Method:"voucher", RateBP:1900, Gross:1190000}` (£11,900) printed as
   `£11,900.0` — last digit gone — because the 5th (Method) column shared
   the same `print.Width=42` budget as the 4 money/rate columns, with no
   fallback once even the tight-collapsed widths didn't fit. The
   accompanying test happened to only exercise the one width (~£1M) where
   the layout does fit, so it "passed" while masking the defect.
   **Fixed:** replaced the 5-column layout with the reviewer's own
   suggested design — group `MethodTaxBands` by method (already sorted
   that way) and print each group as a heading line + the SAME 4-column
   `rateTableRows` table "BY VAT RATE" uses, inheriting its proven ~£1M
   headroom instead of a new, untested budget. Added
   `TestBuildEODDoc_MethodTaxBandOrdinaryAmountNotClipped` (reproduces the
   exact £11,900/7-letter-method case that broke) and
   `TestBuildEODDoc_MethodTaxBandMultipleMethodsGrouped` (proves two
   methods print as separate, correctly-ordered groups, not merged).
2. **NIT (fixed as a real correctness gap, not just re-worded) — the
   per-rate reconciliation identity's "always" was only true when
   `TaxBands` and `MethodTaxBands` came from the same DB snapshot**, but
   `computeEODMethodTaxBands` called `SalesForTaxBands` a *second*,
   independent time rather than sharing `computeEODTaxBands`' read. On
   `/api/reports/eod/range` run on-demand while the shop is still trading,
   a sale completing between the two reads could appear in one breakdown
   and not the other. **Fixed properly, not just documented:** split both
   `computeEODTaxBands`/`computeEODMethodTaxBands` into pure
   `...FromSales(sales)` steps, and added `attachEODBands` — reads
   `SalesForTaxBands` **once** and feeds both pure functions from that one
   snapshot. Rewired both real call sites (`generateEOD`, the range
   handler) to call `attachEODBands` instead of the two separate attach
   calls. `attachEODTaxBands`/`attachEODMethodTaxBands` remain as their
   own independent-read equivalents (existing #1003 tests still call them
   directly, unchanged). Added
   `TestAttachEODBands_MatchesSeparateAttachCalls`, which also proves the
   refactor changed nothing about the math itself.
3. **NIT — the zero-tendered-sale skip's trigger didn't match its own
   comment** (`pos.CompleteSale always has ≥1 payment` is true but doesn't
   imply nonzero *tendered*, e.g. a 100%-discounted sale), and the skip
   was silent, dropping a sale from `MethodTaxBands` while leaving it in
   `TaxBands` with nothing visible. **Fixed:** corrected the comment and
   added a `logging.L().Warnf` so a genuinely hand-broken row is visible.
4. **NIT — the "non-contractual sort order" doc comment was contradicted
   by the tests**, which do assert exact index order. **Fixed:** rewrote
   the comment to say plainly that Method-then-RateBP order *is* asserted.
5. **NIT — `b.Method[:1]` (byte-slicing a label's first character) panics
   on an empty method id and can split a multi-byte character**; this diff
   added a third copy of a pre-existing pattern. **Fixed:** added a
   `titleFirst()` helper (rune-safe) and retired all three copies
   (`rep.Methods`, `rep.Tips`, and the new per-method grouping).
6. **NIT — help prose omitted refunds from the reconciliation rule** in
   all four locales (said a card row group differs from its takings line
   "by exactly the day's card tips", which stops being exact on a day with
   a card refund). **Fixed** in all four (`en`/`tr`/`ar`/`fa` — translated
   in-session, same rule as any new string).
7. **NIT — a dead `if len(out)==0 { return nil, nil }` branch** in the
   method cross-tab (the slice is already nil when empty, declared with
   `var`, never `make()`'d — unlike the sibling `TaxBands` function, which
   genuinely needs the equivalent check). **Fixed:** removed, with a
   comment explaining the asymmetry with `computeEODTaxBandsFromSales`.
8. **Housekeeping** — the orchestrating pipeline session's intermediate
   `WIP(...)` commit message (needed to satisfy a clean-tree requirement
   while the Dev subagent was still writing) is superseded by this
   review's fix-up commits before merge.

## Independently re-verified (not just re-reading the Dev subagent's claims)

- **Rune-vs-byte print-width bug**: reviewer reverted the fix and
  confirmed the existing "BY VAT RATE" wide-amount test fails with the
  literal clipped output, then confirmed it passes again restored.
- **Genuine (non-trivial) flooring exercised**: reviewer hand-verified the
  return-sale test's split (`floor(2000*1140/2140)=1065`, last payment
  gets `935`, not the trivial equal-share `1000`).
- **`pos.CompleteSale` really does require ≥1 payment**: confirmed against
  `internal/pos/sales.go`'s `netPayments` (`errors.New("sale requires at
  least one payment")` on an empty payment slice).
- Full `go build ./...`, `go test ./...` (every package), and
  `guard-data-access.sh` / `guard-i18n.sh` / `guard-help-topics.sh` /
  `guard-compliance-claims.sh` / `guard-kiosk-engine.sh` all re-run and
  green after every fix in this record, not just once at the start.

## Not changed

- The apportionment algorithm itself (floor + last-payment remainder,
  Gross derived never split) was verified correct as designed — no
  changes needed there.
- The tips-exclusion reconciliation rule (`EODMethod.In − EODTip.Amount −
  EODMethod.Out` for a method's column total) was verified correct as
  designed and documented.
