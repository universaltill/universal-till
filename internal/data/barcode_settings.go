package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/universaltill/universal-till/internal/barcode"
)

// BarcodeEnabledSymbologiesKey is the settings-table key holding the shop's
// enabled barcode symbology ids (ADR-0059 Decision §2) as a JSON array of
// internal/barcode registry ids, e.g. ["EAN13","CODE128"]. Stored in the
// existing generic settings table rather than a dedicated table — the ADR
// explicitly leaves the storage shape open ("a JSON column on the existing
// shop-settings row — Dev's call"), and a key/value row needs no migration,
// which is exactly what "defaulting to the set above when absent" wants:
// a shop that never opens the (ut-docs#935) settings checklist has no row
// and behaves exactly as before ADR-0059.
const BarcodeEnabledSymbologiesKey = "barcode_enabled_symbologies"

// ErrEmptyBarcodeSymbologySet is returned by SetBarcodeSymbologyEnabled when
// disabling the given id would leave the shop with zero enabled
// symbologies — every scan and every untyped AddBarcode call would then
// fail to match anything, silently and with no indication why (ut-docs#935
// review finding MAJOR 3: nothing previously stopped a manager unticking
// every box mid-shift). The caller must recover from this — reject the
// change with a message, never let it through.
var ErrEmptyBarcodeSymbologySet = errors.New("at least one barcode symbology must stay enabled")

// DefaultEnabledBarcodeSymbologyIDs returns ADR-0059 §2's default-enabled
// set: every registry entry EXCEPT the two embedded-data ones
// (EAN13_WEIGHT_PREFIX2X / EAN13_PRICE_PREFIX02), which are opt-in — a shop
// that never scanned a scale label must not risk an ordinary EAN-13 being
// misread as a weight/price label. The two excluded ids are fixed registry
// ids (ADR-0059 §1's table); a new embedded-data entry added to the
// registry later must be added here too so it also defaults off.
func DefaultEnabledBarcodeSymbologyIDs() []string {
	all := barcode.Default().IDs()
	out := make([]string, 0, len(all))
	for _, id := range all {
		if id == "EAN13_WEIGHT_PREFIX2X" || id == "EAN13_PRICE_PREFIX02" {
			continue
		}
		out = append(out, id)
	}
	return out
}

// EnabledBarcodeSymbologies returns the shop's enabled symbology ids,
// seeding the setting with the ADR-0059 §2 default set on first read (the
// upgrade path for an existing shop with no row: GetOrCreate writes the
// compatibility-preserving default, so nothing that resolved before stops
// resolving). The default set is ALSO returned alongside any error, so a
// caller that must never block on a settings read (the scan path,
// ADR-0003 offline-first) can use the returned ids regardless — under the
// defaults, behaviour is exactly the pre-ADR-0059 one.
//
// This is the single shared accessor for AddBarcode's inference path, the
// scan-path lookup (POSRepo), the ut-docs#935 settings checklist, and the
// ut-docs#936 catalog-import wiring — do not inline a private copy.
func (r *SettingsRepo) EnabledBarcodeSymbologies(ctx context.Context) ([]string, error) {
	defaults := DefaultEnabledBarcodeSymbologyIDs()
	val, found, err := r.Get(ctx, BarcodeEnabledSymbologiesKey)
	if err != nil {
		return defaults, err
	}
	if !found {
		seed, err := json.Marshal(defaults)
		if err != nil {
			return defaults, fmt.Errorf("marshal default symbologies: %w", err)
		}
		val, err = r.GetOrCreate(ctx, BarcodeEnabledSymbologiesKey, string(seed))
		if err != nil {
			return defaults, err
		}
	}
	var ids []string
	if err := json.Unmarshal([]byte(val), &ids); err != nil {
		// A corrupt row must not brick scanning/barcode entry: fall back to
		// the defaults, surfacing the parse failure to callers that care.
		return defaults, fmt.Errorf("parse setting %s: %w", BarcodeEnabledSymbologiesKey, err)
	}
	return ids, nil
}

// SetEnabledBarcodeSymbologies persists the shop's enabled symbology ids
// verbatim (a full-list replace). Kept for callers that already hold a
// complete, validated list to write; the ut-docs#935 settings checklist
// itself goes through SetBarcodeSymbologyEnabled below instead, which
// read-modify-writes a single id atomically rather than racing a
// stale full-list write against a concurrent toggle.
func (r *SettingsRepo) SetEnabledBarcodeSymbologies(ctx context.Context, ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal enabled symbologies: %w", err)
	}
	return r.Set(ctx, BarcodeEnabledSymbologiesKey, string(b))
}

// SetBarcodeSymbologyEnabled adds or removes id from the shop's enabled
// symbology set, reading the current set and writing the updated one back
// inside a single transaction (ut-docs#935 review finding MAJOR 4: the
// settings checklist has ten independent checkboxes with no hx-sync
// between them, so two toggles issued close together — plausible with a
// ten-item list inviting rapid multi-clicking — must not silently drop one
// another via a read-modify-write race that spans two separate calls).
// Returns the resulting set on success. Returns ErrEmptyBarcodeSymbologySet
// (writing nothing) if this toggle would leave the set empty — never
// silently allowed, since an empty set makes every scan and every untyped
// AddBarcode call fail to match anything.
func (r *SettingsRepo) SetBarcodeSymbologyEnabled(ctx context.Context, id string, enabled bool) ([]string, error) {
	var err error
	done := settingsObs.trace("set_barcode_symbology_enabled")
	defer func() { done(err) }()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("set barcode symbology %s: %w", id, err)
	}
	defer tx.Rollback()

	defaults := DefaultEnabledBarcodeSymbologyIDs()
	ids := defaults
	var val string
	scanErr := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, BarcodeEnabledSymbologiesKey).Scan(&val)
	switch {
	case errors.Is(scanErr, sql.ErrNoRows):
		// No row yet: this toggle is the shop's first-ever change, applied
		// on top of the ADR-0059 §2 defaults — same starting point
		// EnabledBarcodeSymbologies would return.
	case scanErr != nil:
		err = fmt.Errorf("set barcode symbology %s: %w", id, scanErr)
		return nil, err
	default:
		// A corrupt row must not brick this write: fall back to the
		// defaults, same posture as EnabledBarcodeSymbologies.
		_ = json.Unmarshal([]byte(val), &ids)
	}

	newIDs := toggleSymbologyID(ids, id, enabled)
	if len(newIDs) == 0 {
		return nil, ErrEmptyBarcodeSymbologySet
	}

	b, marshalErr := json.Marshal(newIDs)
	if marshalErr != nil {
		err = fmt.Errorf("marshal enabled symbologies: %w", marshalErr)
		return nil, err
	}
	if _, execErr := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, BarcodeEnabledSymbologiesKey, string(b), time.Now().UTC()); execErr != nil {
		err = fmt.Errorf("set barcode symbology %s: %w", id, execErr)
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		err = fmt.Errorf("set barcode symbology %s: %w", id, commitErr)
		return nil, err
	}
	return newIDs, nil
}

// toggleSymbologyID returns ids with id added (enabled=true) or removed
// (enabled=false). Does NOT deduplicate pre-existing duplicates in ids —
// only relevant if ids already contained id more than once, which nothing
// in this package's write path can produce (SetBarcodeSymbologyEnabled is
// the only mutator, and it always starts from a list built the same way).
func toggleSymbologyID(ids []string, id string, enabled bool) []string {
	out := make([]string, 0, len(ids)+1)
	seen := false
	for _, existing := range ids {
		if existing == id {
			seen = true
			if !enabled {
				continue
			}
		}
		out = append(out, existing)
	}
	if enabled && !seen {
		out = append(out, id)
	}
	return out
}
