package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// Single-purpose vouchers (ADR-0105, ut-docs#1037 — §3 Abs. 15 UStG). A
// multi-purpose voucher (ut-docs#1008) is a 0% liability at issue and is
// taxed only when redeemed against whatever the customer buys. A SINGLE-
// PURPOSE voucher is the opposite timing: the goods/service and their VAT
// rate are fixed when the voucher is sold, so VAT is due AT ISSUE — the
// face value is folded into the issuing sale's subtotal/tax at the rate
// stamped on the voucher — and redemption is NOT a taxable event at all
// ("Bei der Einlösung des Einzweck-Gutscheins liegt kein Umsatz vor").
//
// Which kind a shop sells is a shop-level setting (SumUp's shipped
// precedent — never a per-sale staff decision): every voucher is stamped
// with whatever the setting reads at the moment it is issued, immutably.

// Settings keys (ADR-0105 Decision 1). Defined here rather than in
// internal/pages/common because THIS package resolves them at issue time
// (CompleteSale reads the settings table through data.SettingsRepo) and
// cannot import common; common re-exports the ones it also surfaces.
const (
	// SettingKeyVoucherDefaultType: "multi_purpose" (default, absent ==
	// unchanged behaviour) or "single_purpose".
	SettingKeyVoucherDefaultType = "vouchers.default_type"
	// SettingKeyVoucherSinglePurposeTaxRateBP: integer basis points (the
	// same unit as SaleLineInput.TaxRateBasisPoints) a single-purpose
	// voucher is taxed at when sold. Unset -> the shop's standard rate
	// (SettingKeyStoreTaxRate, a whole percent) * 100.
	SettingKeyVoucherSinglePurposeTaxRateBP = "vouchers.single_purpose_tax_rate_bp"
	// SettingKeyStoreTaxRate is the shop's standard rate in whole percent —
	// common.KeyTaxRate, whose value is this constant so the two can never
	// drift.
	SettingKeyStoreTaxRate = "store.tax_rate"
)

// VoucherIssuePolicy is the resolved shop-wide answer to "what kind of
// voucher does a sale issue right now, and at what rate": the effective
// type and, for single_purpose, the basis-point rate.
type VoucherIssuePolicy struct {
	Type      string
	TaxRateBP int
}

// ErrSinglePurposeRateUnconfigured is returned when the shop has switched
// to single-purpose vouchers but no rate can be resolved from either
// SettingKeyVoucherSinglePurposeTaxRateBP or the standard-rate fallback.
// Fail-closed: silently taxing at 0% is the under-declaration this whole
// card exists to prevent. Unreachable through the shipped Settings card
// (it always writes both keys together) — only a hand-edited settings row
// can get here.
var ErrSinglePurposeRateUnconfigured = errors.New("single-purpose vouchers are enabled but no tax rate is configured")

// ResolveVoucherIssuePolicy reads the two ADR-0105 settings and resolves
// the policy in force RIGHT NOW. Absent or unrecognised default_type reads
// as multi_purpose (the pre-ADR-0105 behaviour, and the only value a
// pre-ADR-0105 database can hold); the single-purpose rate falls back to
// the shop's standard rate, and to ErrSinglePurposeRateUnconfigured when
// neither key parses.
func ResolveVoucherIssuePolicy(ctx context.Context, settings *data.SettingsRepo) (VoucherIssuePolicy, error) {
	typ, _, err := settings.Get(ctx, SettingKeyVoucherDefaultType)
	if err != nil {
		return VoucherIssuePolicy{}, fmt.Errorf("resolve voucher issue policy: %w", err)
	}
	if strings.TrimSpace(typ) != data.VoucherTypeSinglePurpose {
		return VoucherIssuePolicy{Type: data.VoucherTypeMultiPurpose}, nil
	}
	if v, ok, err := settings.Get(ctx, SettingKeyVoucherSinglePurposeTaxRateBP); err != nil {
		return VoucherIssuePolicy{}, fmt.Errorf("resolve voucher issue policy: %w", err)
	} else if ok {
		if bp, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && bp >= 0 {
			return VoucherIssuePolicy{Type: data.VoucherTypeSinglePurpose, TaxRateBP: bp}, nil
		}
	}
	if v, ok, err := settings.Get(ctx, SettingKeyStoreTaxRate); err != nil {
		return VoucherIssuePolicy{}, fmt.Errorf("resolve voucher issue policy: %w", err)
	} else if ok {
		if pct, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && pct >= 0 {
			return VoucherIssuePolicy{Type: data.VoucherTypeSinglePurpose, TaxRateBP: pct * 100}, nil
		}
	}
	return VoucherIssuePolicy{}, ErrSinglePurposeRateUnconfigured
}

// resolveVoucherIssueTypes stamps every issue in `issues` that arrived
// without a type (the live tender path — pos_api.go never sets one, per
// ADR-0105 there is no staff-facing type picker) from the policy in force
// now, and validates the ones that arrived stamped (a LAN-sync journal
// replay carrying the issuing till's classification, which must win over
// this till's own current setting). The settings table is read at most
// once, and only when at least one issue needs resolving, so a sale with
// no vouchers pays nothing here.
func resolveVoucherIssueTypes(ctx context.Context, sqlDB *sql.DB, issues []VoucherIssueInput) error {
	var policy *VoucherIssuePolicy
	for i := range issues {
		v := &issues[i]
		v.VoucherType = strings.TrimSpace(v.VoucherType)
		if v.VoucherType == "" {
			if policy == nil {
				p, err := ResolveVoucherIssuePolicy(ctx, data.NewSettingsRepo(sqlDB))
				if err != nil {
					return fmt.Errorf("voucher issue %d: %w", i+1, err)
				}
				policy = &p
			}
			v.VoucherType = policy.Type
			if policy.Type == data.VoucherTypeSinglePurpose {
				v.TaxRateBP = policy.TaxRateBP
			} else {
				v.TaxRateBP = 0
			}
		}
		if err := validateVoucherIssueType(*v); err != nil {
			return fmt.Errorf("voucher issue %d: %w", i+1, err)
		}
	}
	return nil
}

// validateVoucherIssueType is the type/rate shape check computeSaleTotals
// applies to every issue: the type must be one of the two the schema
// admits (an unresolved empty type reaching the totals is a caller bug —
// CompleteSale resolves before computing), and a single-purpose voucher
// needs a non-negative rate.
func validateVoucherIssueType(v VoucherIssueInput) error {
	switch v.VoucherType {
	case data.VoucherTypeMultiPurpose:
		return nil
	case data.VoucherTypeSinglePurpose:
		if v.TaxRateBP < 0 {
			return fmt.Errorf("single-purpose tax rate must be >= 0 basis points, got %d", v.TaxRateBP)
		}
		return nil
	default:
		return fmt.Errorf("invalid voucher type %q", v.VoucherType)
	}
}

// SinglePurposeVoucherVATLine is the ONE synthetic VAT line a single-
// purpose voucher issue contributes, shared by computeSaleTotals (at
// persist time) and the day-close/Tax-tab banding (eod_tax_bands.go, which
// re-derives bands from persisted rows and must inject exactly what the
// engine folded in — ADR-0105 Decision 3). The face value is what the
// customer pays and is therefore the GROSS in BOTH pricing modes, tax
// embedded: a €25 voucher costs €25 whether the shop prices its shelves
// inclusive or exclusive (which is also what keeps `total` unaffected by
// the type, as the ADR requires). So the split is always the inclusive
// one — ComputeTaxBasisPoints(face, rate, true) — never the line loop's
// mode-dependent call. The returned net is what an EXCLUSIVE-mode
// subtotal carries for it (an inclusive subtotal carries the gross).
func SinglePurposeVoucherVATLine(face money.Money, rateBP int) (line VATLine, net money.Money) {
	tax, gross := ComputeTaxBasisPoints(face, rateBP, true)
	return VATLine{RateBP: rateBP, LineTotal: gross.Minor(), TaxAmount: tax.Minor()}, gross.Sub(tax)
}

// singlePurposeRedemptionCheck enforces ADR-0105 Decision 4 for every
// tracked voucher payment in the sale, reading each voucher's row under the
// sale's own transaction (so the type it validates against is the one the
// debit a moment later will see). It returns true when the sale IS a
// single-purpose redemption — the caller then persists the header with
// subtotal/tax_total 0, the mirror image of a multi-purpose issue: VAT was
// collected at issue and redemption adds none.
//
// v1's provable shape is the whole-sale exact match: ONE payment, the
// voucher's own original_amount, equal to the sale's total; every line at
// the voucher's stamped rate; no service charge (a sale-level amount with
// no line, taxed by apportionment) and no voucher issued in the same sale
// (its face would be inside total). A whole-sale discount is fine — the
// customer's gross for these goods is still exactly the face value.
// Anything else is data.ErrVoucherSinglePurposeMismatch, fail-closed, and
// the whole sale rolls back. An unknown voucher id surfaces as
// data.ErrVoucherNotFound here, exactly as DebitVoucherForRedemption would
// have reported it a few statements later.
func singlePurposeRedemptionCheck(ctx context.Context, repo *data.POSRepo, tx *sql.Tx, in SaleInput, total money.Money) (bool, error) {
	isRedemption := false
	for i, p := range in.Payments {
		if p.VoucherID == "" {
			continue
		}
		v, err := repo.GetVoucherBalance(ctx, tx, p.VoucherID)
		if err != nil {
			return false, err
		}
		if v.VoucherType != data.VoucherTypeSinglePurpose {
			continue
		}
		mismatch := func(why string) error {
			return fmt.Errorf("payment %d: voucher %q (%s): %w", i+1, p.VoucherID, why, data.ErrVoucherSinglePurposeMismatch)
		}
		switch {
		case len(in.Payments) != 1:
			return false, mismatch("must be the sole payment, no mixed tender")
		case p.Amount.Minor() != v.OriginalAmountMinor:
			return false, mismatch(fmt.Sprintf("tendered %d, face value %d — no partial redemption", p.Amount.Minor(), v.OriginalAmountMinor))
		case total.Minor() != v.OriginalAmountMinor:
			return false, mismatch(fmt.Sprintf("sale total %d must equal the face value %d", total.Minor(), v.OriginalAmountMinor))
		case len(in.VoucherIssues) > 0:
			return false, mismatch("cannot also issue a voucher in the redeeming sale")
		case in.ServiceCharge.IsPositive():
			return false, mismatch("cannot carry a service charge")
		}
		for j, l := range in.Lines {
			if l.TaxRateBasisPoints != v.TaxRateBP {
				return false, mismatch(fmt.Sprintf("line %d is at %d bp, voucher was taxed at %d bp", j+1, l.TaxRateBasisPoints, v.TaxRateBP))
			}
		}
		isRedemption = true
	}
	return isRedemption, nil
}
