# Code review — single-purpose voucher support (ut-docs#1037)

- **Date:** 2026-09-19
- **Branch:** `feat/1037-single-purpose-vouchers` (reviewed at WIP checkpoint `d33274b0d`, branched from `main` @ `445bb01ea`)
- **Reviewer:** independent adversarial pass by a different model than the implementing Dev (Fable), per the pipeline's Reviewer step
- **Verdict:** **Safe to merge** after the one blocker below was fixed in this branch.

## What shipped

Extends the multi-purpose voucher liability model (ut-docs#1008) with a second kind —
**single-purpose** vouchers (Einzweck-Gutscheine, §3 Abs. 13-15 UStG), where the good and
therefore the VAT rate are already known at issue, so VAT is due on the *issuing* sale
rather than deferred to redemption.

- **Migration `036_single_purpose_vouchers.sql`** — two additive `ALTER TABLE vouchers ADD COLUMN`
  statements: `purpose TEXT NOT NULL DEFAULT 'multi_purpose' CHECK (...)` and
  `issue_vat_rate_bp INTEGER` (nullable, so a genuine 0% rate stays distinguishable from
  "no rate"). Checksum pinned in `internal/db/shipped_migrations_test.go`.
- **`internal/data/voucher_repo.go`** — `Voucher.Purpose` / `IssueVATRateBP *int`; new
  `RedeemSinglePurposeVoucher` (standalone drain + `voucher_transactions` row, never touches
  sales tables); the **double-tax guard** added to the existing `DebitVoucherForRedemption`;
  the Z-report liability readers now exclude single-purpose rows entirely.
- **`internal/pos/sales.go`** — `computeSaleTotals` split into "part 1" (validation + the
  single-purpose tax, before `VATBandsForSale`) and "part 2" (the unchanged multi-purpose
  fold, after the negative clamp).
- **`internal/pages/`** — new `POST /api/vouchers/{id}/redeem`; single-purpose issues re-added
  as VAT lines for the Z-report bands, the method×rate cross-tab and the invoice VAT table;
  LAN-sync journal contract bumped to 1.10.0 with a 409 `voucher_not_multi_purpose` reason.
- **UI / i18n / manual** — purpose radio pair + conditional VAT-rate field on "Sell a voucher",
  a two-step Check-then-Redeem affordance; 8 new keys in all 4 shipped UI locales
  (en/ar/fa/tr); a real new manual section in all 5 help locales (en/de/ar/fa/tr).
- 19 new tests, plus 3 added by this review.

## Findings

### BLOCKER (fixed in this branch) — a single-purpose voucher sale could not be completed at all under tax-EXCLUSIVE pricing

`internal/pages/pos_api.go` accumulated **every** voucher issue — both kinds — into
`voucherIssueTotal`, and then added that flat face value to the total it demands:

```go
total = total.Add(voucherIssueTotal)   // face value, no tax
```

That is correct for a multi-purpose voucher (a 0% liability that rides on top of the taxed
total after the clamp) but wrong for a single-purpose one, which `pos.computeSaleTotals`
taxes into `subtotal`/`tax_total`. Under **exclusive** pricing the two therefore disagreed by
exactly the voucher's VAT: the handler demanded the bare face value while `CompleteSale`
computed face value + VAT, so `CompleteSale` rejected the handler's own figure and the sale
failed outright with *"Amount received does not cover the sale total"*. A secondary, smaller
drift existed in the same block: `chargeTax` was apportioned over
`ChargeTaxLinesFromSale(saleLines)` only, while `computeSaleTotals` weighs the single-purpose
voucher's rate band too, so a sale carrying both a service charge and such a voucher could
disagree by a rounding unit as well.

The client had the same defect independently: `web/public/app.js`'s `voucherIssueTotal()`
summed `issue.amount` flat, so the split-tender panel *quoted* the short figure to the
cashier, who would then be refused on Complete Sale.

Under **inclusive** pricing the face value already contains the VAT, so the two happen to
agree — and every one of the 19 shipped tests, plus the Tester's end-to-end driven run,
priced tax-inclusive. That is precisely why this survived to review.

**Fixed** in three places:
- `internal/pages/pos_api.go` — single-purpose issues now collect into `singlePurposeIssues`
  and are folded into the taxed base and into the service charge's apportionment lines,
  mirroring `computeSaleTotals`. Deliberately folded *after* `serviceCharge` itself is
  computed, so the charge's own base stays the lines-only post-discount subtotal the basket
  engine quotes on screen.
- `internal/pages/index_page.go` + `web/ui/pages/index.html` — the sale's pricing mode is
  exposed to the client as `data-tax-inclusive`.
- `web/public/app.js` — new `issueGross()` adds the VAT on top for a single-purpose issue
  under exclusive pricing, mirroring `pos.ComputeTaxBasisPoints`'s exclusive branch (half-up)
  so the two round identically.

Three regression tests added (`internal/pages/voucher_single_purpose_test.go`), asserting the
demanded amount equals the persisted sale total under exclusive pricing, with and without a
service charge, and that the multi-purpose path is unchanged.

### Accepted / no change needed

- **Multi-purpose path is behaviourally unchanged.** Re-derived from the function's own
  comments and verified: the moved validation (count cap, amount > 0, per-voucher ceiling) is
  pure input checking with no dependence on anything computed later; `chargeLines` is
  byte-identical to `ChargeTaxLinesFromSale(in.Lines)` when no single-purpose voucher is
  present; the overflow ceiling checks, the negative-total clamp, the clamp-then-fold ordering
  and the ADR-0061 service-charge tax ordering are all untouched for that path. The only
  observable difference is *error precedence*: a malformed voucher issue now surfaces before a
  negative `ServiceCharge` error. Both inputs are rejected either way, no accepted sale changes
  value. Not a regression against the "byte-for-byte unchanged" bar.
- **Sale discount prorates over a single-purpose voucher** (it does not for multi-purpose,
  by explicit design there). This is deliberate and defensible — a single-purpose voucher *is*
  the supply of the good, so discounting it is an ordinary discounted sale and VAT is charged
  on the consideration actually received — but it means the customer can pay less than the
  balance the voucher is created with. Consistent between engine, Z-report and invoice
  (all feed the same `VATLine` set through the same `VATBandsForSale`). Noted, not changed.
- **`invoice_page.go` skips a single-purpose issue whose `issue_vat_rate_bp` is NULL**, whereas
  the EOD reader `COALESCE(..., 0)`s it into a 0% band. Unreachable — `CompleteSale` always
  writes the rate for a single-purpose voucher — and both are defensive guards against a
  hand-edited row. Left as-is.
- **`WHERE purpose = 'multi_purpose'` defense-in-depth reports a generic error** if it is ever
  the thing that fires (see TDD note below). Unreachable because the pre-check fires first.

## What I verified beyond running the automated tests

**Migration correctness.** Genuinely additive: two `ADD COLUMN`s, no `ALTER`/`DROP` touching an
existing column or constraint, no 12-step table rebuild — so the `voucher_transactions.voucher_id`
→ `vouchers(id)` FK is not re-fired and no child rows can be orphaned (the exact failure class
`003_kitchen_station_display_flag.sql` documents). `NOT NULL DEFAULT 'multi_purpose'` backfills
every pre-existing row, so no legacy voucher can read as non-multi and be refused as payment —
which would have been a silent, catastrophic regression given the new guard. Version `036` is
unique (`guard-migration-version-collision` passes). **Checksum pinned independently verified,
not trusted**: appending a statement to the file made `TestShippedMigrationsUnchanged` fail with
`pinned e625b0a2…, got c1c218a6…`; restoring made it pass. The pin matches the bytes on disk.

**Double-tax guard traced on every redemption path, not just the tested ones.**
- *Local tender* — `pos/sales.go:1189` → `DebitVoucherForRedemption`. Guarded.
- *Cross-till reservation* — `ReserveVoucherRedemption` → the same debit. Guarded; the primary
  returns 409 `voucher_not_multi_purpose`, and `reserveVoucherOnPrimary` treats it as a
  *definitive* refusal, never a "fall back to local" case — so a replica with no local row
  cannot tender it.
- *LAN-sync journal replay* — the guard sits **before** the `force` branch and is repeated in
  **both** UPDATE predicates, so `force=true` (replay) cannot bypass it. I confirmed the wire
  contract is complete in *both* directions: the producer (`data.SaleDetail`, `pos_repo.go`)
  really does read `v.purpose`/`v.issue_vat_rate_bp` and emit them, and `applyJournal` really
  does reconstruct them — so a replica-issued single-purpose voucher replays on the primary as
  single-purpose at the same rate, rather than silently becoming a 0% liability. An adversarial
  journal declaring an unknown `purpose` is rejected by `computeSaleTotals`'s closed vocabulary.
- *Replica mirror* — `EnsureVoucherLocalRow` defaults an empty `Purpose` to multi, but that path
  is only reached after the primary has already accepted the reservation, and a pre-#1037 primary
  has no single-purpose vouchers to mis-mirror.
- *Scan-to-tender* — `pos_api.go` refuses a scanned single-purpose voucher before it is offered
  as a pay-grid option.

**Auth on the new write endpoint.** Verified independently rather than trusting the code comment:
`/api/vouchers/*` appears nowhere in `internal/auth/middleware.go`'s exemption switch or prefix
list, so `POST /api/vouchers/{id}/redeem` — which irreversibly drains a voucher — is not
anonymously reachable.

**Money safety.** All tax derives from the single shared `ComputeTaxBasisPoints` (integer
`MulDiv`, half-up), called with the same amount and the same `TaxInclusive` flag at issue and at
every re-derivation, so no fractional minor unit can arise and the Z-report/invoice reproduce the
engine's figures exactly. `money.Money` ↔ `int64` conversion happens only at DB/DTO boundaries
via `FromMinor`/`Minor`. Rate basis points stay `int`, matching the established convention in
`internal/pos` (`VATLine.RateBP`, `ChargeTaxLine.RateBP`, `ComputeTaxBasisPoints`), not money.
`IssueVATRateBP` is a `*int` so a real 0% rate survives the round trip. My JS mirror stays well
inside `Number.MAX_SAFE_INTEGER` at the enforced `MaxVoucherIssueAmount` ceiling.

**Band identities re-derived by hand** for inclusive and exclusive, with and without a sale
discount and a service charge: `sum(band.Tax) == TaxNet` and
`sum(band.Gross) == Net − multi-purpose vouchers issued` both hold with both kinds on one day,
because the single-purpose issue is added to the bands *and* excluded from the GUTSCHEINE
figure. `InferTaxInclusive`'s identity still holds (the single-purpose amount is in `subtotal`
and not in `voucher_issue_total`, so it does not double-count).

**Recurring bug classes.** No new file-write handler (so no missing `os.MkdirAll`) and no
cwd-relative path where `paths.Data(...)` belongs — this change is DB/HTTP-only. Confirmed by
scanning the diff for `os.Create`/`WriteFile`/`OpenFile`/`MkdirAll`/`filepath.Join`: no hits.

**Manual and screenshots.** `web/help/*/vouchers.md` gained a real, substantive section in all
five help locales — not a stub: what the two kinds are, how to sell one, how to redeem one, and
a "what can go wrong" list. The docs-shots claim ("124 shots, no PNG changed") holds up: the
`vouchers` topic has no screenshots at all (only `voucher-import` does), which is why no topic
hash moved, and `e2e/tests-docs/docs-shots.spec.ts` only seeds basket lines on the `sell` topic —
it never opens the Split tab or the collapsed `<details>` that holds the new controls, so no
rendered pixel changes. My own edits are pixel-neutral for the same reason (an invisible data
attribute, a JS arithmetic helper, a template key, server-side math), so I refreshed the surface
hash via `scripts/ci/update-docs-shots-surface-hash.sh`; `git diff` on `manifest.json` shows only
`surface_sha256` changing.

**i18n.** `guard-i18n.sh` passes (1806 keys resolve, all locales match `en.json`, no duplicate
keys). The locale split is correct and not confused: core ships **4** UI locales (en/ar/fa/tr —
`web/locales/` has exactly those) and **5** help locales (en/de/ar/fa/tr — `de` is help-only).
I read the ar/fa/tr strings rather than trusting the report: they are genuine, not transliterated
or machine-shaped — Arabic uses correct tashkeel (`سلِّم`, `تُحتسب`) and Arabic guillemets `«»`;
Persian uses ZWNJ correctly (`نمی‌توان`, `هر استفاده‌ای`) and the ezafe hamza (`زبانهٔ`); Turkish
applies the apostrophe-before-case-suffix rule on a UI name (`Kullan'a`), which machine
translation routinely gets wrong. Each locale's voucher noun matches the term already used
throughout that file. New CSS uses logical properties only — no `left`/`right` — so RTL is safe.

**Demo data / secrets.** Only `"Sample Holder"` appears as a holder label — a generic
placeholder, no real client or shop name anywhere in the diff. No secret-shaped literals.

### TDD re-verification (my own, by reverting the fix on disk)

Four reverts, each run and then restored; the full suite is green again afterwards and
`git diff` confirms no residue in the reverted files.

1. **Double-tax guard** — removed the `purpose != VoucherPurposeMulti` pre-check in
   `DebitVoucherForRedemption`. `TestVoucherRepo_TenderDebitRejectsSinglePurpose` failed:
   `debit force=false: err = voucher "GS-SP-D": voucher balance does not cover the tendered
   amount, want ErrVoucherNotMultiPurpose`. This also proved the `WHERE purpose = 'multi_purpose'`
   predicate is genuine defense in depth — the voucher was still not debited — though it reports
   a misleading error if it is ever the thing that fires.
2. **`computeSaleTotals` single-purpose branch** — reverted the tax-at-issue fold.
   `TestComputeSaleTotals_SinglePurposeVoucherTaxedAtIssue` failed in both sub-cases
   (`subtotal 0 tax 0 voucherIssueTotal 2500 total 2500, want 2500/399/0/2500` inclusive;
   `want 2500/475/0/2975` exclusive) and `TestCompleteSale_SinglePurposeVoucherIssueGolden`
   failed on the persisted row — i.e. the VAT genuinely vanishes into the 0% liability without it.
3. **EOD band adapter** — removed the single-purpose loop from `eodVATLinesForSale`.
   `TestEODTaxBands_SinglePurposeVoucherIssueThroughCompleteSale` failed with
   `expected 1 band, got []`.
4. **My own fix** — reverted `pos_api.go`. Both new exclusive-pricing tests failed with
   `tender refused its own demanded amount — the handler's total disagrees with
   pos.computeSaleTotals`, while the multi-purpose test stayed green, confirming the fix is
   correctly scoped to single-purpose. Restored: green.

### Gate re-run after the fix

`gofmt -l` clean · `go build ./...` clean · `go vet ./...` clean · `go test ./...` **all packages
pass** · `golangci-lint run ./...` **0 issues** · all CI-blocking guards in `ci.yml`'s `build`
job pass individually (`guard-i18n`, `guard-data-access`, `guard-help-topics`, `guard-help-drift`,
`guard-docs-shots`, `guard-compliance-claims`, `guard-kiosk-engine`, `guard-page-http-error`,
`guard-migration-version-collision`, `guard-htmx-loaded`, `guard-autofill-suppression`,
`guard-emoji-font`, `guard-plugin-menu-read`, `guard-e2e-fixtures-import`, `check-brand-assets`,
`guard-makefile-version`, `guard-osk-loaded`).

Two guards cannot run in this container and were confirmed **environment-only**, matching the
Tester's report: `guard-deadcode-baseline` needs GTK/WebKit headers (`Package 'gtk+-3.0' … not
found`; pre-existing on `main` too) and `guard-shellcheck-version` needs a `shellcheck` binary
that is not installed here — no `.sh` file is touched by this branch or by my fix.

## Known, accepted gaps (carried forward, not re-litigated)

1. **Language packs need follow-up PRs.** The 8 new `web/locales/en.json` keys
   (`tender.issue_voucher.purpose`, `.purpose_multi`, `.purpose_single`, `.vat_rate`,
   `tender.redeem_voucher`, `tender.status.voucher_vat_rate_invalid`,
   `pos.toast.voucher_single_purpose_tender`, `pos.toast.voucher_redeemed_single`) must be added
   to the external **`ut-plugin-language-de`** and **`ut-plugin-language-es`** pack repos.
   Those repos are outside this session's GitHub access entirely and cannot be reached from here.
   `lang-pack-drift` is advisory-only on the PR and **blocking on push to `main`**, so these
   follow-up PRs are required to keep `main` green.
2. **Single-purpose redemption is local-only.** A replica with no local voucher row returns 404
   rather than proxying to the primary, deliberately, to avoid a cross-till double-redeem race
   (draining a replica's mirror while the primary still shows `active`). Confirmed **actually
   documented**, not merely claimed: in the code, on the handler in
   `internal/pages/voucher_api.go` ("Local-only by design — unlike the balance check, this never
   proxies to the primary … The cross-till write-through for this kind is a follow-up, same shape
   as ADR-0084's for multi-purpose"), and for the shop owner in the manual —
   `web/help/en/vouchers.md`: *"A specific-item voucher sold on another till can be checked from
   this one but not yet redeemed here — redeem it on the main till or on the till that sold it."*
   A follow-up card should track the cross-till write-through.

## Verdict

**Safe to merge.** One genuine blocker found and fixed (single-purpose voucher sales were
impossible to complete under tax-exclusive pricing, on both the server's demanded total and the
client's quoted total), covered by three new regression tests. The multi-purpose path is
behaviourally unchanged. The migration is safely additive with an independently verified
checksum, and the double-tax guard holds on every redemption path including forced journal replay.
