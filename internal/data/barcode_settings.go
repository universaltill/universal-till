package data

import (
	"context"
	"encoding/json"
	"fmt"

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
// (the ut-docs#935 settings checklist writes through this).
func (r *SettingsRepo) SetEnabledBarcodeSymbologies(ctx context.Context, ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("marshal enabled symbologies: %w", err)
	}
	return r.Set(ctx, BarcodeEnabledSymbologiesKey, string(b))
}
