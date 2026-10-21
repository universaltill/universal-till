package catimport

import (
	"fmt"
	"slices"
	"strings"
)

// Mapping is a shop's own column choice for a generic file (ut-docs#4550):
// a neutral field (MappableFields) → the file's header that holds it, as
// HeaderRow returned it. An empty header means "don't read this field",
// which also drops a column the synonyms matched on their own. Fields the
// mapping doesn't name keep their synonym match, except one whose column
// the mapping gave to another field: one column feeds one field.
//
// A nil or empty Mapping parses exactly as before. A mapping is for
// generic files only (FormatGeneric, FormatGenericERP): a known export's
// layout is fixed, and its detection signature must keep meaning what it
// says (MappingIssueNotGeneric).
type Mapping map[string]string

// mappableFields are the neutral columns a Mapping may set, in the order a
// mapping screen lists them. Format-only fields (formatOnlyFields) are not
// mappable: they exist to read one vendor's export.
var mappableFields = []string{
	"name", "price", "sku", "barcode", "category", "department",
	"description", "tax", "takeaway_tax", "stock", "track_stock",
	"weighed", "age_restricted", "item_type", "net_quantity",
	"net_quantity_unit",
}

// MappableFields returns the fields a Mapping may set, in display order.
func MappableFields() []string {
	return append([]string(nil), mappableFields...)
}

// MappingError reason codes: stable, so a caller translates them instead
// of showing parser text (ut-docs#303).
const (
	MappingIssueUnknownField    = "unknown_field"    // Field isn't in MappableFields
	MappingIssueHeaderNotFound  = "header_not_found" // no header in the file folds to Header
	MappingIssueHeaderAmbiguous = "header_ambiguous" // two or more headers fold to Header
	MappingIssueHeaderReused    = "header_reused"    // Header is mapped to two fields
	MappingIssueNotGeneric      = "not_generic"      // the file is a known export (Field is empty)
)

// MappingError is why a Mapping can't be applied to a file.
type MappingError struct {
	Code   string // a MappingIssue* code
	Field  string
	Header string
}

func (e *MappingError) Error() string {
	return fmt.Sprintf("column mapping: %s (field %q, header %q)", e.Code, e.Field, e.Header)
}

// ValidateMapping reports whether m can be applied to a file with this
// header row, as Parse/ParseXLSX would apply it: nil, or a *MappingError
// for the first problem found (fields in MappableFields order). It doesn't
// check that the result has a name column; Parse's ErrNoNameColumn does.
func ValidateMapping(headers []string, m Mapping) error {
	if len(headers) > 0 && strings.HasPrefix(headers[0], "\uFEFF") {
		headers = append([]string(nil), headers...)
		headers[0] = strings.TrimPrefix(headers[0], "\uFEFF")
	}
	_, err := mappedIndex(headers, DetectFormat(headers), m)
	return err
}

// IsGenericFormat reports whether a Result.Format takes a Mapping: a
// generic file, not a known export (MappingIssueNotGeneric).
func IsGenericFormat(format string) bool {
	return format == FormatGeneric || format == FormatGenericERP
}

// maxMappingHeaderBytes bounds a mapped header: it comes from a web
// request, and HeaderRow never returns a cell this long from a real file.
const maxMappingHeaderBytes = 1024

// mappedIndex is headerIndex with m applied on top. headers must already
// have a leading BOM stripped. The synonyms are matched again with the
// mapped columns blanked, so a field whose first synonym match the mapping
// took falls back to its next one ("Title,Name" with Title mapped away
// still finds Name).
func mappedIndex(headers []string, format string, m Mapping) (map[string]int, error) {
	if len(m) == 0 {
		return headerIndex(headers), nil
	}
	if !IsGenericFormat(format) {
		return nil, &MappingError{Code: MappingIssueNotGeneric}
	}
	known := make(map[string]bool, len(mappableFields))
	for _, f := range mappableFields {
		known[f] = true
	}
	var unknown []string
	for field := range m {
		if !known[field] {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, &MappingError{Code: MappingIssueUnknownField, Field: truncate(unknown[0])}
	}
	positions := map[string][]int{}
	for i, h := range headers {
		k := foldHeader(h)
		positions[k] = append(positions[k], i)
	}
	mapped := map[string]int{}  // field → column; -1 = not read
	claimed := map[int]string{} // column → field
	for _, field := range mappableFields {
		h, ok := m[field]
		if !ok {
			continue
		}
		if len(h) > maxMappingHeaderBytes {
			return nil, &MappingError{Code: MappingIssueHeaderNotFound, Field: field, Header: truncate(h)}
		}
		k := foldHeader(h)
		if k == "" {
			mapped[field] = -1
			continue
		}
		cols := positions[k]
		switch {
		case len(cols) == 0:
			return nil, &MappingError{Code: MappingIssueHeaderNotFound, Field: field, Header: h}
		case len(cols) > 1:
			return nil, &MappingError{Code: MappingIssueHeaderAmbiguous, Field: field, Header: h}
		}
		if _, taken := claimed[cols[0]]; taken {
			return nil, &MappingError{Code: MappingIssueHeaderReused, Field: field, Header: h}
		}
		mapped[field] = cols[0]
		claimed[cols[0]] = field
	}
	rest := append([]string(nil), headers...)
	for col := range claimed {
		rest[col] = "" // matches no synonym in either pass
	}
	idx := headerIndex(rest)
	for field, col := range mapped {
		if col < 0 {
			delete(idx, field)
		} else {
			idx[field] = col
		}
	}
	return idx, nil
}

// truncate keeps an untrusted value short enough for an error message.
func truncate(s string) string {
	if len(s) <= 64 {
		return s
	}
	return strings.ToValidUTF8(s[:64], "") + "…"
}
