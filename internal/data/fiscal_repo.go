package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/logging"
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
// Abs. 4 AO till-notification duty (ut-docs#665) — the data the shop's own
// Mein ELSTER filing needs, joined with the register's and its stock
// location's display names, and the location's address (all added
// specifically for this feature). Data capture only: nothing here files
// anything on the shop's behalf. Since ADR-0072 (ut-docs#1106, migration
// 075) the persisted half lives as a JSON blob in plugin_storage under the
// German tax plugin's namespace (see FiscalRegisterDEStore below); the
// Register*/Location* display fields are resolved live at read time and are
// never stored.
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

// RegisterLocationRow is one register's identity plus its stock location's
// display/address fields (empty strings when the register has no location,
// or the location has no address on file yet).
type RegisterLocationRow struct {
	RegisterID       string
	RegisterName     string
	LocationID       string
	LocationName     string
	LocationStreet   string
	LocationPostcode string
	LocationCity     string
}

// ListRegisterLocations returns EVERY register — active or not — with its
// location joined in (LEFT JOIN + COALESCE, same shape the old
// fiscal_register_de list query used). It backs FiscalRegisterDEStore.List's
// read-time join (ADR-0072): a decommissioned till's register is routinely
// deactivated afterwards, and its history must still show the register's
// name, so ListRegisters' is_active=1 filter is deliberately not reused.
func (r *POSRepo) ListRegisterLocations(ctx context.Context) ([]RegisterLocationRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT reg.id, reg.name,
       COALESCE(loc.id, ''), COALESCE(loc.name, ''),
       COALESCE(loc.address_street, ''), COALESCE(loc.address_postcode, ''), COALESCE(loc.address_city, '')
FROM registers reg
LEFT JOIN stock_locations loc ON loc.id = reg.location_id
`)
	if err != nil {
		return nil, fmt.Errorf("list register locations: %w", err)
	}
	defer rows.Close()
	var out []RegisterLocationRow
	for rows.Next() {
		var row RegisterLocationRow
		if err := rows.Scan(&row.RegisterID, &row.RegisterName,
			&row.LocationID, &row.LocationName, &row.LocationStreet, &row.LocationPostcode, &row.LocationCity); err != nil {
			return nil, fmt.Errorf("scan register location: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate register locations: %w", err)
	}
	return out, nil
}

// FiscalRegisterDEKeyPrefix namespaces the entries inside the plugin's KV
// storage: one key per entry, FiscalRegisterDEKeyPrefix + entry id. Exported
// so internal/plugins can preserve this data on automatic uninstall paths
// (ADR-0072 review finding B1) without duplicating the literal.
const FiscalRegisterDEKeyPrefix = "fiscal_register:"

// fiscalRegisterDERecord is the JSON shape persisted in plugin_storage —
// exactly the columns migration 059's table carried, nothing more (the
// join-derived Register*/Location* display fields are resolved live by
// List, never stored). The json tags MUST stay in lockstep with migration
// 075's json_object(...) keys: that INSERT..SELECT is the one-shot path
// every pre-075 row round-trips through, and a mismatched key there would
// silently drop the field on unmarshal.
type fiscalRegisterDERecord struct {
	ID                 string  `json:"id"`
	RegisterID         string  `json:"register_id"`
	EasType            string  `json:"eas_type"`
	EasSoftware        string  `json:"eas_software"`
	EasSerial          string  `json:"eas_serial"`
	TSESerial          string  `json:"tse_serial"`
	TSECertificationID string  `json:"tse_certification_id"`
	TSEType            string  `json:"tse_type"`
	AcquiredOn         string  `json:"acquired_on"`
	CommissionedOn     *string `json:"commissioned_on"`
	DecommissionedOn   *string `json:"decommissioned_on"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

// FiscalRegisterDEStore persists §146a Abs. 4 AO till/TSE entries as JSON
// blobs in the plugin_storage KV table, namespaced under the German tax
// plugin's id (ADR-0072, ut-docs#1106) — the plugin owns the obligation's
// data (it vanishes with the plugin's uninstall housekeeping, DeleteStorage),
// while the page and its handlers stay in core. The caller passes the plugin
// id (internal/pages' taxDePluginID) rather than this package redefining the
// constant. At a shop's realistic volume (single digits to low tens of
// entries, ever) the KV scan replaces the old table's index comfortably;
// ADR-0072 explicitly flags this as NOT a pattern for row-count-growth data.
type FiscalRegisterDEStore struct {
	db       *sql.DB
	pos      *POSRepo
	plugin   *PluginRepo
	pluginID string
}

// NewFiscalRegisterDEStore builds the store over one *sql.DB — POSRepo and
// PluginRepo both wrap the same handle, so this is wiring, not a second
// connection.
func NewFiscalRegisterDEStore(db *sql.DB, pluginID string) *FiscalRegisterDEStore {
	return &FiscalRegisterDEStore{db: db, pos: NewPOSRepo(db), plugin: NewPluginRepo(db), pluginID: pluginID}
}

// Create records one till/TSE pairing. registerID must already exist — the
// old table's FK caught a typo'd/stale id at write time; plugin_storage has
// no FK onto registers, so the same guarantee is an explicit existence check
// here (no is_active filter: the FK never checked that either, and the
// page's picker is the UI-level active-only filter). created_at/updated_at
// are stamped now (RFC3339 UTC); the business dates stay whatever
// caller-supplied YYYY-MM-DD strings they were validated as upstream.
func (s *FiscalRegisterDEStore) Create(ctx context.Context, registerID, easType, easSoftware, easSerial,
	tseSerial, tseCertificationID, tseType, acquiredOn string, commissionedOn *string) (string, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM registers WHERE id = ?`, registerID).Scan(&n); err != nil {
		return "", fmt.Errorf("create fiscal register de: register %s: %w", registerID, err)
	}
	if n == 0 {
		return "", fmt.Errorf("create fiscal register de: register %s: not found", registerID)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	rec := fiscalRegisterDERecord{
		ID: id, RegisterID: registerID,
		EasType: easType, EasSoftware: easSoftware, EasSerial: easSerial,
		TSESerial: tseSerial, TSECertificationID: tseCertificationID, TSEType: tseType,
		AcquiredOn: acquiredOn, CommissionedOn: commissionedOn,
		CreatedAt: now, UpdatedAt: now,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("create fiscal register de: marshal: %w", err)
	}
	// StorageSet enforces the plugin-storage caps (ErrStorageTooLarge); the
	// page surfaces any create error through its one generic error key,
	// which reads fine for the cap too — no dedicated error path.
	if err := s.plugin.StorageSet(ctx, s.pluginID, FiscalRegisterDEKeyPrefix+id, raw); err != nil {
		return "", fmt.Errorf("create fiscal register de: %w", err)
	}
	return id, nil
}

// List returns every recorded entry with its register's name and (via the
// register's own location) its stock location's name and address joined in
// at read time. A register with no assigned location, or a location with no
// address on file yet, still appears — with empty location fields — rather
// than being silently dropped. Ordered exactly as the old SQL was: location
// name first (an entry whose register has NO location sorts last — the old
// CASE WHEN loc.name IS NULL; LocationID == "" is that same "no joined
// location row" condition, and a real location's genuinely-empty name still
// sorts first within the located group, matching SQL's ” < everything),
// then register name, then acquired_on. SliceStable keeps equal-key entries
// in ListStorageByPrefix's key order, so full ties stay deterministic.
func (s *FiscalRegisterDEStore) List(ctx context.Context) ([]FiscalRegisterDE, error) {
	entries, err := s.plugin.ListStorageByPrefix(ctx, s.pluginID, FiscalRegisterDEKeyPrefix)
	if err != nil {
		return nil, fmt.Errorf("list fiscal register de: %w", err)
	}
	regs, err := s.pos.ListRegisterLocations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list fiscal register de: %w", err)
	}
	byRegister := make(map[string]RegisterLocationRow, len(regs))
	for _, r := range regs {
		byRegister[r.RegisterID] = r
	}

	out := make([]FiscalRegisterDE, 0, len(entries))
	for _, e := range entries {
		var rec fiscalRegisterDERecord
		if err := json.Unmarshal(e.Value, &rec); err != nil {
			// Skip-and-log, not abort-the-whole-page (ADR-0072/ut-docs#1106
			// review finding S3): plugin_storage under this plugin's
			// namespace is also writable by the plugin's own WASM guest
			// code (hostStorageSet writes under the calling plugin's id),
			// so one malformed or maliciously-crafted key must not make
			// every other shop's genuine AO entry unreachable. Same
			// precedent as export_repo.go's unparseable-content_json skip.
			logging.L().Warnf("fiscal register de: list: skipping unparseable entry %s: %v", e.Key, err)
			continue
		}
		reg, ok := byRegister[rec.RegisterID]
		if !ok {
			// The old SQL INNER JOINed registers, silently dropping an entry
			// whose register was hard-deleted. Registers are soft-deleted
			// only, so this is unreachable in practice — preserved as a skip,
			// not promoted to an error (ADR-0072).
			continue
		}
		out = append(out, FiscalRegisterDE{
			ID: rec.ID, RegisterID: rec.RegisterID, RegisterName: reg.RegisterName,
			LocationID: reg.LocationID, LocationName: reg.LocationName,
			LocationStreet: reg.LocationStreet, LocationPostcode: reg.LocationPostcode, LocationCity: reg.LocationCity,
			EasType: rec.EasType, EasSoftware: rec.EasSoftware, EasSerial: rec.EasSerial,
			TSESerial: rec.TSESerial, TSECertificationID: rec.TSECertificationID, TSEType: rec.TSEType,
			AcquiredOn: rec.AcquiredOn, CommissionedOn: rec.CommissionedOn, DecommissionedOn: rec.DecommissionedOn,
			CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if aNoLoc, bNoLoc := a.LocationID == "", b.LocationID == ""; aNoLoc != bNoLoc {
			return bNoLoc // located entries before no-location ones
		}
		if a.LocationName != b.LocationName {
			return a.LocationName < b.LocationName
		}
		if a.RegisterName != b.RegisterName {
			return a.RegisterName < b.RegisterName
		}
		return a.AcquiredOn < b.AcquiredOn
	})
	return out, nil
}

// Decommission stamps decommissioned_on on one entry and bumps updated_at.
// It never deletes the key — the AO record must stay visible after the till
// goes out of service (migration 059's own "destroys nothing" discipline,
// reaffirmed by ADR-0072: decommission is an update, never a delete), so the
// page above this only ever flips a status, never removes the entry.
func (s *FiscalRegisterDEStore) Decommission(ctx context.Context, id, decommissionedOn string) error {
	key := FiscalRegisterDEKeyPrefix + id
	raw, err := s.plugin.StorageGet(ctx, s.pluginID, key)
	if errors.Is(err, ErrStorageNotFound) {
		// Same shape as the old zero-rows-UPDATE error.
		return fmt.Errorf("decommission fiscal register de: %s not found", id)
	}
	if err != nil {
		return fmt.Errorf("decommission fiscal register de: %w", err)
	}
	var rec fiscalRegisterDERecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("decommission fiscal register de: unmarshal %s: %w", id, err)
	}
	rec.DecommissionedOn = &decommissionedOn
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	updated, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("decommission fiscal register de: marshal %s: %w", id, err)
	}
	if err := s.plugin.StorageSet(ctx, s.pluginID, key, updated); err != nil {
		return fmt.Errorf("decommission fiscal register de: %w", err)
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
