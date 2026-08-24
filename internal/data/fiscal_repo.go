package data

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// FiscalTSESignature is one signed sale's §6 KassenSichV TSE evidence
// (ut-docs#585) — the optional `tse` object a fiscal.sign.ask plugin may
// return alongside "approved" (contract fiscal-sign-ask.md v1.1.0), stored
// verbatim in fiscal_tse_signatures (migration 048). Timestamps stay the
// RFC3339 strings the plugin sent; the signature stays base64. These are
// evidence of what the TSE said, not values core derives or normalizes.
type FiscalTSESignature struct {
	SaleID             string
	TransactionNumber  int64
	SignatureCounter   int64
	SerialNumber       string
	StartTime          string
	LogTime            string
	Signature          string
	SignatureAlgorithm string
	// CreatedAt is stamped by the DB on insert; zero on the way in.
	CreatedAt string
}

// RecordFiscalTSESignature stores one sale's TSE evidence. Idempotent by
// design (INSERT ... ON CONFLICT DO NOTHING on the sale_id primary key): a
// duplicated bookkeeping call for the same sale never errors, duplicates, or
// overwrites the first recorded evidence.
func (r *POSRepo) RecordFiscalTSESignature(ctx context.Context, sig FiscalTSESignature) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO fiscal_tse_signatures
	(sale_id, transaction_number, signature_counter, serial_number, start_time, log_time, signature, signature_algorithm)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(sale_id) DO NOTHING
`, sig.SaleID, sig.TransactionNumber, sig.SignatureCounter, sig.SerialNumber,
		sig.StartTime, sig.LogTime, sig.Signature, sig.SignatureAlgorithm)
	if err != nil {
		return fmt.Errorf("insert fiscal_tse_signatures: %w", err)
	}
	return nil
}

// GetFiscalTSESignature loads the TSE evidence recorded for a sale.
// (nil, false, nil) when none exists — not an error: both receipt render
// paths treat "no evidence" as "no TSE block", never placeholder text.
func (r *POSRepo) GetFiscalTSESignature(ctx context.Context, saleID string) (*FiscalTSESignature, bool, error) {
	var sig FiscalTSESignature
	err := r.db.QueryRowContext(ctx, `
SELECT sale_id, transaction_number, signature_counter, serial_number, start_time, log_time, signature, signature_algorithm, created_at
FROM fiscal_tse_signatures
WHERE sale_id = ?
`, saleID).Scan(&sig.SaleID, &sig.TransactionNumber, &sig.SignatureCounter, &sig.SerialNumber,
		&sig.StartTime, &sig.LogTime, &sig.Signature, &sig.SignatureAlgorithm, &sig.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("select fiscal_tse_signatures: %w", err)
	}
	return &sig, true, nil
}

// FiscalRegisterDE is one till/TSE pairing recorded for Germany's §146a
// Abs. 4 AO till-notification duty (ut-docs#665, migration 059) — the data
// the shop's own Mein ELSTER filing needs, joined with the register's and
// its stock location's display names, and the location's address (all
// added specifically for this feature). Data capture only: nothing here
// files anything on the shop's behalf.
type FiscalRegisterDE struct {
	ID                 string
	RegisterID         string
	RegisterName       string
	LocationID         string
	LocationName       string
	LocationStreet     string
	LocationPostcode   string
	LocationCity       string
	EasType            string
	EasSoftware        string
	EasSerial          string
	TSESerial          string
	TSECertificationID string
	TSEType            string
	AcquiredOn         string
	CommissionedOn     *string
	DecommissionedOn   *string
	CreatedAt          string
	UpdatedAt          string
}

// CreateFiscalRegisterDE records one till/TSE pairing. registerID must
// already exist — the FK on fiscal_register_de.register_id catches a
// typo'd/stale id, wrapped here into a clear error rather than a raw driver
// message. created_at/updated_at are stamped now (RFC3339 UTC); the four
// business dates (acquiredOn/commissionedOn and, later, decommissionedOn)
// stay whatever caller-supplied YYYY-MM-DD strings they were validated as
// upstream — this method does not reinterpret them.
func (r *POSRepo) CreateFiscalRegisterDE(ctx context.Context, registerID, easType, easSoftware, easSerial,
	tseSerial, tseCertificationID, tseType, acquiredOn string, commissionedOn *string) (string, error) {
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO fiscal_register_de
	(id, register_id, eas_type, eas_software, eas_serial, tse_serial, tse_certification_id, tse_type,
	 acquired_on, commissioned_on, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, id, registerID, easType, easSoftware, easSerial, tseSerial, tseCertificationID, tseType,
		acquiredOn, commissionedOn, now, now); err != nil {
		return "", fmt.Errorf("create fiscal register de: register %s: %w", registerID, err)
	}
	return id, nil
}

// ListFiscalRegisterDE returns every recorded entry, LEFT JOINed to its
// register's name and (via the register's own location_id) its stock
// location's name and address. A register with no assigned location, or a
// location with no address on file yet, still appears — with empty
// location fields — rather than being silently dropped from the list.
// Ordered by location name (an unassigned register's empty location name
// sorts last), then register name, then acquired_on, so entries group
// naturally by location for the page above this.
func (r *POSRepo) ListFiscalRegisterDE(ctx context.Context) ([]FiscalRegisterDE, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT f.id, f.register_id, reg.name,
       COALESCE(loc.id, ''), COALESCE(loc.name, ''),
       COALESCE(loc.address_street, ''), COALESCE(loc.address_postcode, ''), COALESCE(loc.address_city, ''),
       f.eas_type, f.eas_software, f.eas_serial,
       f.tse_serial, f.tse_certification_id, f.tse_type,
       f.acquired_on, f.commissioned_on, f.decommissioned_on,
       f.created_at, f.updated_at
FROM fiscal_register_de f
JOIN registers reg ON reg.id = f.register_id
LEFT JOIN stock_locations loc ON loc.id = reg.location_id
ORDER BY CASE WHEN loc.name IS NULL THEN 1 ELSE 0 END, loc.name, reg.name, f.acquired_on
`)
	if err != nil {
		return nil, fmt.Errorf("list fiscal register de: %w", err)
	}
	defer rows.Close()
	var out []FiscalRegisterDE
	for rows.Next() {
		var f FiscalRegisterDE
		var commissionedOn, decommissionedOn sql.NullString
		if err := rows.Scan(&f.ID, &f.RegisterID, &f.RegisterName,
			&f.LocationID, &f.LocationName, &f.LocationStreet, &f.LocationPostcode, &f.LocationCity,
			&f.EasType, &f.EasSoftware, &f.EasSerial,
			&f.TSESerial, &f.TSECertificationID, &f.TSEType,
			&f.AcquiredOn, &commissionedOn, &decommissionedOn,
			&f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fiscal register de: %w", err)
		}
		if commissionedOn.Valid {
			v := commissionedOn.String
			f.CommissionedOn = &v
		}
		if decommissionedOn.Valid {
			v := decommissionedOn.String
			f.DecommissionedOn = &v
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fiscal register de: %w", err)
	}
	return out, nil
}

// DecommissionFiscalRegisterDE stamps decommissioned_on on one entry and
// bumps updated_at. It never deletes or hides the row — the AO record must
// stay visible after the till goes out of service, so the page above this
// only ever flips a status, never removes the entry from the list.
func (r *POSRepo) DecommissionFiscalRegisterDE(ctx context.Context, id, decommissionedOn string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx,
		`UPDATE fiscal_register_de SET decommissioned_on = ?, updated_at = ? WHERE id = ?`,
		decommissionedOn, now, id)
	if err != nil {
		return fmt.Errorf("decommission fiscal register de: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("decommission fiscal register de: %s not found", id)
	}
	return nil
}

// SetStockLocationAddressDE sets a stock location's street/postcode/city —
// added specifically for the §146a Abs. 4 AO register (ELSTER's form asks
// for the till's place of business), independent of everything else a
// stock location already does (inventory, movements, register assignment).
// All three are accepted as given, including empty — no field is
// individually required.
func (r *POSRepo) SetStockLocationAddressDE(ctx context.Context, locationID, street, postcode, city string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE stock_locations SET address_street = ?, address_postcode = ?, address_city = ? WHERE id = ?`,
		street, postcode, city, locationID)
	if err != nil {
		return fmt.Errorf("set stock location address de: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set stock location address de: %s not found", locationID)
	}
	return nil
}
