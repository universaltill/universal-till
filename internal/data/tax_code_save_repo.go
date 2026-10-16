package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/taxrate"
)

// TaxCodeSave is a create-or-update of one tax code by id, nil = keep —
// the cloud catalogue pull's tax_code row (ADR-0162 §4, ut-docs#4463).
// TakeawayBP sets the takeaway rate; ClearTakeaway removes it (no
// override). A takeaway rate equal to the dine-in rate is stored as no
// override, the tax-code form's canonical rule (parseTaxCodeForm).
type TaxCodeSave struct {
	ID            string
	Create        bool
	Name          *string
	RateBP        *int
	TakeawayBP    *int
	ClearTakeaway bool
	Active        *bool
}

// TaxCodeSaveResult reports what SaveTaxCodeTx did.
type TaxCodeSaveResult struct {
	Created bool
	Name    string
	Changed []string
}

// SaveTaxCodeTx applies s inside the caller's transaction with the rules
// the tax-code screen applies (name required and unique, rates 0–100 %),
// plus refuseRates — the caller's per-country rate rule
// (fiscal.AllowedTaxRateSetBP via the pages layer), nil = none — checked on
// the resulting pair. An unknown id without Create is ErrTaxCodeNotFound;
// a taken name is ErrTaxCodeNameExists.
func (r *CatalogRepo) SaveTaxCodeTx(ctx context.Context, tx *sql.Tx, s TaxCodeSave, refuseRates func(rateBP int, takeawayBP *int) bool) (TaxCodeSaveResult, error) {
	var res TaxCodeSaveResult
	s.ID = strings.TrimSpace(s.ID)
	if s.ID == "" {
		return res, errors.New("missing id")
	}
	if s.Name != nil {
		n := strings.TrimSpace(*s.Name)
		if n == "" {
			return res, errors.New("the tax code name must not be blank")
		}
		s.Name = &n
	}
	for _, bp := range []*int{s.RateBP, s.TakeawayBP} {
		if bp != nil && (*bp < 0 || *bp > taxrate.MaxBP) {
			return res, fmt.Errorf("a tax rate must be between 0 and %d basis points", taxrate.MaxBP)
		}
	}
	if s.ClearTakeaway && s.TakeawayBP != nil {
		return res, errors.New("a takeaway rate cannot be both set and cleared")
	}

	var name string
	var rate int
	var takeaway sql.NullInt64
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT name, rate_basis_points, takeaway_rate_basis_points, is_active FROM tax_codes WHERE id = ?`, s.ID).
		Scan(&name, &rate, &takeaway, &active)
	exists := true
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if !s.Create {
			return res, ErrTaxCodeNotFound
		}
		if s.Name == nil || s.RateBP == nil {
			return res, errors.New("a new tax code needs a name and a rate")
		}
		exists, active, res.Created = false, true, true
	case err != nil:
		return res, fmt.Errorf("save tax code: load: %w", err)
	}
	var takeawayBP *int
	if takeaway.Valid {
		v := int(takeaway.Int64)
		takeawayBP = &v
	}
	if s.Name != nil {
		name = *s.Name
		res.Changed = append(res.Changed, "name")
	}
	if s.RateBP != nil {
		rate = *s.RateBP
		res.Changed = append(res.Changed, "rate_basis_points")
	}
	if s.TakeawayBP != nil {
		v := *s.TakeawayBP
		takeawayBP = &v
		res.Changed = append(res.Changed, "takeaway_rate_basis_points")
	} else if s.ClearTakeaway {
		takeawayBP = nil
		res.Changed = append(res.Changed, "takeaway_rate_basis_points")
	}
	if takeawayBP != nil && *takeawayBP == rate {
		takeawayBP = nil
	}
	if s.Active != nil {
		active = *s.Active
		res.Changed = append(res.Changed, "active")
	}
	if refuseRates != nil && refuseRates(rate, takeawayBP) {
		return res, errors.New("this tax rate is not allowed in the shop's country")
	}
	if exists {
		_, err = tx.ExecContext(ctx, `
UPDATE tax_codes SET name = ?, rate_basis_points = ?, takeaway_rate_basis_points = ?, is_active = ?
WHERE id = ?`, name, rate, nullableIntPtr(takeawayBP), boolToInt(active), s.ID)
	} else {
		_, err = tx.ExecContext(ctx, `
INSERT INTO tax_codes (id, name, rate_basis_points, takeaway_rate_basis_points, is_active)
VALUES (?, ?, ?, ?, ?)`, s.ID, name, rate, nullableIntPtr(takeawayBP), boolToInt(active))
	}
	if err != nil {
		if isUniqueViolation(err) {
			return res, ErrTaxCodeNameExists
		}
		return res, fmt.Errorf("save tax code: %w", err)
	}
	res.Name = name
	return res, nil
}
