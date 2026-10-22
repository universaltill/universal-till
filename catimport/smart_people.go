package catimport

import (
	"bytes"
	"encoding/csv"
	"io"
	"math"
	"strconv"
	"strings"
)

// Customer is one parsed customer row (ADR-0168 §5). The till is the only
// store of customers: this type exists in memory for the preview and the
// sealed hand-off, never in a database of the cloud.
type Customer struct {
	Row      int
	Name     string
	Email    string
	Phone    string
	Postcode string
	Address  string
	City     string
	Country  string
}

// CustomersResult is a parsed customers file.
type CustomersResult struct {
	Source    string
	Kind      string
	Customers []Customer
	Issues    []RowIssue
}

// StockRow is one counted line of a stock file (ADR-0168 §4).
type StockRow struct {
	Row      int
	SKU      string
	Barcode  string
	Name     string
	Quantity float64
	Weighed  bool // the file's own sold-by-weight column said yes
}

// StockResult is a parsed stock file.
type StockResult struct {
	Source string
	Kind   string
	Rows   []StockRow
	Issues []RowIssue
}

// newRows opens the data records of an already-read upload.
func newRows(buf []byte, comma rune) (*csv.Reader, error) {
	cr := csv.NewReader(bytes.NewReader(buf))
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	if _, err := cr.Read(); err != nil {
		return nil, err
	}
	return cr, nil
}

func cell(rec []string, col int) string {
	if col < 0 || col >= len(rec) {
		return ""
	}
	return StripCSVDefuse(strings.TrimSpace(rec[col]))
}

func allEmpty(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// ParseCustomers reads a customers file of any source. A name is one
// column or first + last; a row with no name is an error issue and stays
// in the list so it can be corrected or skipped; a repeated e-mail
// (case-folded) is flagged on the later row with "clear it" as the
// suggestion. A fully blank row is ignored. Epos Now's own customers
// export is not a built-in shape until a real header row exists
// (ADR-0168 §1): such a file reads through these aliases.
func ParseCustomers(r io.Reader, o Options) (CustomersResult, error) {
	buf, headers, comma, err := readSmart(r)
	if err != nil {
		return CustomersResult{}, err
	}
	if err := gate(headers, KindCustomers, o); err != nil {
		return CustomersResult{}, err
	}
	full := colFor(headers, aliasFullName)
	first := colFor(headers, aliasFirstName, full)
	last := colFor(headers, aliasLastName, full, first)
	email := colFor(headers, aliasEmail)
	phone := colFor(headers, aliasPhone)
	post := colFor(headers, aliasPostcode)
	addr := colFor(headers, aliasAddress)
	city := colFor(headers, aliasCity)
	country := colFor(headers, aliasCountry)
	cr, err := newRows(buf, comma)
	if err != nil {
		return CustomersResult{}, err
	}
	res := CustomersResult{Source: DetectSource(headers), Kind: KindCustomers}
	seenEmail := map[string]int{}
	row := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		row++
		if err != nil {
			return res, rowErr(row, err)
		}
		if allEmpty(rec) {
			continue
		}
		c := Customer{
			Row:      row,
			Name:     cell(rec, full),
			Email:    cell(rec, email),
			Phone:    cell(rec, phone),
			Postcode: cell(rec, post),
			Address:  cell(rec, addr),
			City:     cell(rec, city),
			Country:  cell(rec, country),
		}
		if c.Name == "" {
			c.Name = strings.TrimSpace(cell(rec, first) + " " + cell(rec, last))
		}
		add := func(code, sev, field, orig string, sug *string) {
			res.Issues = append(res.Issues, RowIssue{Row: row, Code: code, Severity: sev, Field: field, Original: orig, Suggested: sug})
		}
		if c.Name == "" {
			add(IssueCodeMissingName, SeverityError, "name", "", nil)
		}
		if c.Email != "" {
			if !validEmail(c.Email) {
				var sug *string
				if g := guessEmail(c.Email); g != "" {
					sug = sp(g)
				}
				add(IssueCodeBadEmail, SeverityError, "email", c.Email, sug)
			} else {
				k := strings.ToLower(c.Email)
				if _, dup := seenEmail[k]; dup {
					add(IssueCodeDuplicateEmail, SeverityWarning, "email", c.Email, sp(""))
				} else {
					seenEmail[k] = row
				}
			}
		}
		res.Customers = append(res.Customers, c)
	}
	return res, nil
}

func rowErr(row int, err error) error {
	return &rowError{row: row, err: err}
}

type rowError struct {
	row int
	err error
}

func (e *rowError) Error() string { return "row " + strconv.Itoa(e.row) + ": " + e.err.Error() }
func (e *rowError) Unwrap() error { return e.err }

func validEmail(s string) bool {
	if strings.ContainsAny(s, " \t,;<>()") {
		return false
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') {
		return false
	}
	dom := s[at+1:]
	dot := strings.LastIndexByte(dom, '.')
	return dot > 0 && dot < len(dom)-1 && !strings.HasPrefix(dom, ".")
}

// guessEmail repairs the usual slips (a mailto: prefix, spaces, a trailing
// full stop or comma); "" when the repair still is not an address.
func guessEmail(s string) string {
	g := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "mailto:")
	g = strings.ReplaceAll(g, " ", "")
	g = strings.TrimRight(g, ".,;")
	if g != s && validEmail(g) {
		return g
	}
	return ""
}

// ParseStock reads a stock file: SKU and/or barcode plus a counted
// quantity (ADR-0168 §4). A decimal quantity is an error unless the row is
// sold by weight (the file's own column or Options.IsWeighed); a missing
// key, a repeated key, a blank, unreadable or negative quantity are error
// issues too. Rows stay in the list so they can be corrected or skipped.
func ParseStock(r io.Reader, o Options) (StockResult, error) {
	buf, headers, comma, err := readSmart(r)
	if err != nil {
		return StockResult{}, err
	}
	if err := gate(headers, KindStock, o); err != nil {
		return StockResult{}, err
	}
	sku := colFor(headers, aliasSKU)
	bc := colFor(headers, aliasBarcode)
	if sku < 0 && hasColumn(headers, "sku") {
		sku = findSynonymCol(headers, "sku")
	}
	if bc < 0 && hasColumn(headers, "barcode") {
		bc = findSynonymCol(headers, "barcode")
	}
	qty := colFor(headers, aliasQty)
	if qty < 0 {
		qty = findSynonymCol(headers, "stock")
	}
	name := colFor(headers, aliasName)
	weighed := colFor(headers, aliasWeighed)
	cr, err := newRows(buf, comma)
	if err != nil {
		return StockResult{}, err
	}
	res := StockResult{Source: DetectSource(headers), Kind: KindStock}
	seen := map[string]bool{}
	row := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		row++
		if err != nil {
			return res, rowErr(row, err)
		}
		if allEmpty(rec) {
			continue
		}
		sr := StockRow{Row: row, SKU: cell(rec, sku), Name: cell(rec, name)}
		if raw := cell(rec, bc); raw != "" {
			if dec, ok := normalizeBarcode(raw, o.EnabledSymbologyIDs); ok {
				sr.Barcode = dec.LookupKey
			} else {
				sr.Barcode = raw
			}
		}
		if g, ok := parseBoolLoose(cell(rec, weighed)); ok && g.val {
			sr.Weighed = true
		}
		add := func(code, field, orig string, sug *string) {
			res.Issues = append(res.Issues, RowIssue{Row: row, Code: code, Severity: SeverityError, Field: field, Original: orig, Suggested: sug})
		}
		key := sr.SKU
		if key == "" {
			key = sr.Barcode
		}
		switch {
		case key == "":
			add(IssueCodeMissingKey, "sku", "", nil)
		case seen[key]:
			add(IssueCodeDuplicateKey, "sku", key, nil)
		default:
			seen[key] = true
		}
		raw := cell(rec, qty)
		switch q, ok := parseQuantity(raw); {
		case raw == "":
			add(IssueCodeMissingQuantity, "quantity", "", nil)
		case !ok:
			add(IssueCodeBadNumber, "quantity", raw, nil)
		case q < 0:
			sr.Quantity = q
			add(IssueCodeNegativeQuantity, "quantity", raw, sp("0"))
		default:
			sr.Quantity = q
			if q != math.Trunc(q) && !sr.Weighed && !(o.IsWeighed != nil && o.IsWeighed(sr.SKU, sr.Barcode)) {
				add(IssueCodeDecimalQuantity, "quantity", raw, sp(strconv.FormatFloat(math.Round(q), 'f', 0, 64)))
			}
		}
		res.Rows = append(res.Rows, sr)
	}
	return res, nil
}

// findSynonymCol is the column headerIndex's English synonyms give field.
func findSynonymCol(headers []string, field string) int {
	if i, ok := headerIndex(headers)[field]; ok {
		return i
	}
	return -1
}

// parseQuantity reads a counted quantity. With both "." and "," the last
// one is the decimal mark ("1.234,5" DE/TR, "1,234.5" EN); a mark that
// repeats is a thousands separator ("1.000.000"); a single "," is a
// thousands separator when exactly three digits follow it ("1,000"), else
// a decimal comma ("2,5"); a single "." is the decimal point (review M1).
func parseQuantity(raw string) (float64, bool) {
	s := strings.ReplaceAll(strings.TrimSpace(raw), " ", "")
	if s == "" {
		return 0, false
	}
	dot, comma := strings.LastIndexByte(s, '.'), strings.LastIndexByte(s, ',')
	switch {
	case dot >= 0 && comma >= 0 && dot > comma:
		s = strings.ReplaceAll(s, ",", "")
	case dot >= 0 && comma >= 0:
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	case comma >= 0 && (strings.Count(s, ",") > 1 || len(s)-comma-1 == 3):
		s = strings.ReplaceAll(s, ",", "")
	case comma >= 0:
		s = strings.ReplaceAll(s, ",", ".")
	case strings.Count(s, ".") > 1:
		s = strings.ReplaceAll(s, ".", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
