// Package barcode provides a shop-configurable barcode symbology parser and
// registry (ADR-0059). It is a leaf package: it depends on nothing beyond
// the standard library and internal/money (for the price-embedded decode),
// and never imports internal/data or internal/pages — the data layer and
// the scan path both import this package, not the other way round.
//
// Adding a symbology means adding a new Registry entry in registry.go, not
// a new branch in a caller (ADR-0059 Decision §1's "data, not a new if"
// requirement). This card (ut-docs#933) ships the parser/registry only —
// wiring it into AddBarcode and the scan path is ut-docs#934.
package barcode

import "github.com/universaltill/universal-till/internal/money"

// Decoded is the result of a Symbology successfully parsing a scanned code.
type Decoded struct {
	// SymbologyID is the id of the Symbology that produced this decode.
	SymbologyID string

	// LookupKey is the string to match against item_barcodes.barcode /
	// variant_barcodes.barcode. For a plain symbology this is the scanned
	// code itself, verbatim — not normalised/expanded to a canonical form.
	// This matters concretely for UPC-E: LookupKey is the 8-digit
	// compressed code the scanner actually sent, not its 12-digit UPC-A
	// expansion, so a catalog barcode entered as the expanded UPC-A form
	// will not match a UPC-E scan of the same product (ut-docs#933 review
	// finding F3) — #934/#935 need to decide, deliberately, whether to
	// normalise plain-symbology lookup keys before this is relied on for
	// a real till.
	//
	// For an embedded-data symbology it is the fixed prefix+item-key
	// portion of the code (digits 1-7) with the variable embedded digits
	// and the check digit — both of which depend on the specific
	// weight/price a label carries, and so can never be part of a stable
	// key — zeroed out (see EAN13_WEIGHT_PREFIX2X / EAN13_PRICE_PREFIX02 in
	// registry.go for the exact layout). A shop's catalog barcode for a
	// scale-label item must be entered using this same zeroed convention
	// for scan-time lookup to ever match it; this is the contract
	// ut-docs#934/#935 build against.
	LookupKey string

	// HasEmbeddedPrice and EmbeddedPrice are set only when SymbologyID is
	// EAN13_PRICE_PREFIX02 — the barcode encodes an absolute price for that
	// one specific unit, not a per-unit rate (ADR-0059 Decision §3).
	// EmbeddedPrice can legitimately be zero (a label printed with a zero
	// price) — callers must not treat HasEmbeddedPrice==true, Price==0 as
	// a parse failure, and #934 must decide deliberately whether a
	// zero-price scan is allowed to create a basket line at all.
	HasEmbeddedPrice bool
	EmbeddedPrice    money.Money

	// HasEmbeddedWeight and EmbeddedWeight are set only when SymbologyID is
	// EAN13_WEIGHT_PREFIX2X. EmbeddedWeight is a decimal string in
	// kilograms (e.g. "1.234") — a quantity, not money, so it deliberately
	// does not use internal/money (it ultimately feeds BasketLine.Qty, a
	// float64). Like EmbeddedPrice, it can legitimately be "0.000".
	HasEmbeddedWeight bool
	EmbeddedWeight    string
}

// Symbology is one barcode family: a stable id, an i18n display-name key
// for the settings checklist (ut-docs#935), and a parser.
type Symbology struct {
	// ID is the canonical registry id (e.g. "EAN13"). Callers store this
	// verbatim into barcode_type.
	ID string

	// NameKey is the i18n key for this symbology's display name —
	// barcode.symbology.<ID> (ADR-0059 Decision §2). This package never
	// renders it; the key exists so the settings checklist and this
	// registry can't drift on what a symbology is called or how many there
	// are.
	NameKey string

	// embedsData is true for a symbology that carries a price or weight in
	// the barcode itself (the two ADR-0059 embedded-data entries). Match
	// tries embedded-data entries before plain ones — see Match below.
	embedsData bool

	// Parse attempts to parse code as this symbology, returning ok=false if
	// code does not satisfy this symbology's validation rules.
	Parse func(code string) (Decoded, bool)
}

// Registry holds a set of Symbology entries and matches a scanned code
// against a shop's enabled subset in specificity order.
type Registry struct {
	entries []Symbology // declaration order, for IDs()/Lookup() stability
}

// IDs returns every registry entry's id, in declaration order.
func (r *Registry) IDs() []string {
	ids := make([]string, len(r.entries))
	for i, s := range r.entries {
		ids[i] = s.ID
	}
	return ids
}

// Lookup returns the entry with the given id.
func (r *Registry) Lookup(id string) (Symbology, bool) {
	for _, s := range r.entries {
		if s.ID == id {
			return s, true
		}
	}
	return Symbology{}, false
}

// Match tries to parse code against the entries named in enabledIDs, trying
// every embedded-data entry before any plain entry (ADR-0059 Decision §3's
// specificity order): a valid embedded-data code is also a structurally
// valid plain EAN-13, so trying plain entries first would make the two
// embedded-data entries unreachable — they'd never get a chance to match
// before plain EAN13 already consumed the code. Entries not present in
// enabledIDs, or not in the registry at all, are never attempted.
//
// Within a tier, entries are tried in registry declaration order, and —
// unlike the embedded/plain split above — that order can decide the
// outcome (ut-docs#933 review finding F1): CODE128/CODE39/INTERNAL_PLU are
// deliberately permissive catch-alls with no checksum, so Default()
// declares them after every checksum/structurally-validated plain entry
// (EAN13/EAN8/UPCA/UPCE/GTIN14) specifically so those more-specific
// entries get first refusal on a code, and the catch-alls only ever see
// what nothing more specific matched. Two entries can still genuinely tie
// within the checksum-validated group itself — EAN8 and UPC-E are both 8
// digits and both checksum-validated, and most valid UPC-E codes are also
// structurally valid EAN-8 (finding F2) — declaration order breaks that
// tie (EAN8 wins) but does not remove the ambiguity; LookupKey is
// unaffected either way, only the recorded SymbologyID can be "wrong" for
// such a code.
//
// AddBarcode and the scan-path lookup (ut-docs#934) both call this one
// function so the ordering is defined once, not duplicated per caller.
func (r *Registry) Match(enabledIDs []string, code string) (Decoded, bool) {
	enabled := make(map[string]bool, len(enabledIDs))
	for _, id := range enabledIDs {
		enabled[id] = true
	}
	if d, ok := r.matchTier(enabled, code, true); ok {
		return d, true
	}
	return r.matchTier(enabled, code, false)
}

func (r *Registry) matchTier(enabled map[string]bool, code string, embedded bool) (Decoded, bool) {
	for _, s := range r.entries {
		if s.embedsData != embedded || !enabled[s.ID] {
			continue
		}
		if d, ok := s.Parse(code); ok {
			return d, true
		}
	}
	return Decoded{}, false
}
