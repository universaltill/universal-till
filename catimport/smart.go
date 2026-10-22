package catimport

// Smart import (ADR-0168, ut-docs#4744/#4747): one parser for the three
// kinds of file a shop uploads (products, stock counts, customers), the
// source system and the kind both detected from the header row, EN + DE +
// TR header aliases, and row issues that carry a suggestion (§3a) so the
// owner never has to fix the file and re-upload. Everything here is a pure
// parser: no database, no locale (reason codes only, ut-docs#303).

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Kinds a file can hold.
const (
	KindProducts  = "products"
	KindStock     = "stock"
	KindCustomers = "customers"
)

// Sources DetectSource names besides the grandfathered Format* values.
// FormatEposNow is the first built-in vendor shape after ADR-0164's guard
// (ADR-0168 §1): only formats with a real export in hand are added.
const (
	SourceEposNow    = FormatEposNow
	SourceTillExport = "till-export"
	SourceMyExport   = "my-export"
	SourceGeneric    = FormatGeneric
)

// FormatEposNow is Result.Format for the Epos Now products export.
const FormatEposNow = "eposnow" // core-neutral:allow ADR-0168 §1 built-in shape from a real export (#4747)

// Whole-file refusal codes (KindDetection.Refusal, KindError.Code).
const (
	RefusalMixedKinds     = "mixed_kinds"     // customer columns together with a price column
	RefusalUnknownKind    = "unknown_kind"    // matches no kind; MissingByKind names the columns
	RefusalMissingColumns = "missing_columns" // the asked-for kind lacks required columns
)

// Severity values of a RowIssue.
const (
	SeverityError   = "error"   // the row cannot be imported until corrected or skipped
	SeverityWarning = "warning" // the row imports; the suggestion is the default fix
)

// Row issue codes (ADR-0168 §3a). Stable, machine-readable: the pages
// layer composes the translated message.
const (
	IssueCodeMissingName      = "missing_name"
	IssueCodeBadPrice         = "bad_price"
	IssueCodeMissingPrice     = "missing_price"
	IssueCodeZeroPrice        = "zero_price"
	IssueCodeBadNumber        = "bad_number"
	IssueCodeBadBoolean       = "bad_boolean"
	IssueCodeUnknownCategory  = "unknown_category"
	IssueCodeDuplicateBarcode = "duplicate_barcode"
	IssueCodeMissingBarcode   = "missing_barcode"
	IssueCodeMissingSKU       = "missing_sku"
	IssueCodeDuplicateSKU     = "duplicate_sku"
	IssueCodeNoSymbology      = "no_symbology_match"
	IssueCodeUnknownTaxRate   = "unknown_tax_rate"
	IssueCodeAmbiguousVAT     = "ambiguous_vat"
	IssueCodeBadTax           = "bad_tax"
	IssueCodeNameTooLong      = "name_too_long"

	IssueCodeBadEmail       = "bad_email"
	IssueCodeDuplicateEmail = "duplicate_email"

	IssueCodeMissingKey       = "missing_key"
	IssueCodeDuplicateKey     = "duplicate_key"
	IssueCodeMissingQuantity  = "missing_quantity"
	IssueCodeDecimalQuantity  = "decimal_quantity"
	IssueCodeNegativeQuantity = "negative_quantity"
)

// NameMaxRunes is the longest item name a row keeps; a longer one is an
// IssueCodeNameTooLong with the shortened text as its suggestion.
const NameMaxRunes = 120

// maxSmartFileBytes bounds what the smart parsers read into memory.
const maxSmartFileBytes = 64 << 20

// ErrFileTooLarge is returned when a file passes maxSmartFileBytes.
var ErrFileTooLarge = errors.New("catimport: file too large")

// RowIssue is one problem with one row (ADR-0168 §3a). Row is the record
// number, header = 1. Suggested is nil when there is no suggestion, a
// pointer to "" when the suggestion is to clear the field. Alternatives
// are filled by the caller (it knows the store's categories and rates).
type RowIssue struct {
	Row          int      `json:"row"`
	Code         string   `json:"code"`
	Severity     string   `json:"severity"`
	Field        string   `json:"field"`
	Original     string   `json:"original"`
	Suggested    *string  `json:"suggested,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
}

func sp(s string) *string { return &s }

// Options carries the caller's knowledge into the parsers. The zero value
// parses with 0 currency decimals and no store context.
type Options struct {
	CurrencyDecimals         int
	EnabledSymbologyIDs      []string
	UseItemNumbersAsBarcodes bool
	// Mapping is the shop's column choice for a generic products file
	// (ut-docs#4550); the built-in shapes ignore it.
	Mapping Mapping
	// KnownTaxRatesBP are the store's live rates in basis points. With
	// them an Epos Now rate is snapped to one of them; without, the
	// group id or the derived rate is used as it reads.
	KnownTaxRatesBP []int
	// KnownCategories, when non-nil, are the store's category names: a
	// non-empty category outside them is an unknown_category issue
	// (suggestion: create it). nil = not checked.
	KnownCategories []string
	// IsWeighed lets ParseStock accept a decimal quantity for an item the
	// caller knows is sold by weight; the file's own column also counts.
	IsWeighed func(sku, barcode string) bool
	// Force skips the mixed/unknown refusal when the operator chose the
	// kind by hand in the preview; required columns are still checked.
	Force bool
}

// KindDetection is DetectKind's answer.
type KindDetection struct {
	Kind          string   // "" when refused
	Matched       []string // neutral fields found, e.g. "name", "price"
	Missing       []string // required columns the detected kind lacks (empty when detected)
	MissingByKind map[string][]string
	Refusal       string // "" or RefusalMixedKinds / RefusalUnknownKind
}

// KindError refuses a whole file; nothing is imported (ADR-0168 §2).
type KindError struct {
	Code      string // a Refusal* code
	Kind      string // the kind asked for (RefusalMissingColumns)
	Missing   []string
	Detection KindDetection
}

func (e *KindError) Error() string {
	return fmt.Sprintf("catimport: %s (kind %q, missing %v)", e.Code, e.Kind, e.Missing)
}

// foldKey folds a header for alias matching: case, accents and the
// Turkish dotted/dotless i, then everything that is not a letter or digit
// is dropped ("E-Mail" == "email", "Satış Fiyatı" == "satis fiyati").
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimPrefix(s, "\ufeff") {
		r = unicode.ToLower(r)
		switch r {
		case 'ı', 'İ':
			r = 'i'
		case 'ü':
			r = 'u'
		case 'ö':
			r = 'o'
		case 'ş':
			r = 's'
		case 'ç':
			r = 'c'
		case 'ğ':
			r = 'g'
		case 'ä':
			r = 'a'
		case 'ß':
			b.WriteString("ss")
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type aliasSet map[string]bool

func aliases(list ...string) aliasSet {
	s := aliasSet{}
	for _, a := range list {
		s[foldKey(a)] = true
	}
	return s
}

// EN + DE + TR header aliases, neutral (ADR-0168 §2). Written in any
// spelling; foldKey makes them match "Ürün Adı", "URUN ADI" and "urun_adi".
var (
	aliasName     = aliases("name", "item name", "product name", "title", "bezeichnung", "artikelbezeichnung", "artikelname", "produktname", "produkt", "artikel", "ürün adı", "ürün ismi", "ürün", "isim")
	aliasPrice    = aliases("price", "default price", "retail price", "sale price", "selling price", "unit price", "preis", "verkaufspreis", "vk-preis", "einzelpreis", "fiyat", "satış fiyatı", "birim fiyat", "ürün fiyatı")
	aliasSKU      = aliases("sku", "reference", "item code", "product code", "artikelnummer", "artikelnr", "artikel-nr", "artikelcode", "stok kodu", "ürün kodu", "stok no")
	aliasBarcode  = aliases("barcode", "barcodes", "gtin", "upc", "ean", "strichcode", "barkod", "barkod no")
	aliasCategory = aliases("category", "category name", "group", "class", "kategorie", "warengruppe", "gruppe", "kategori", "grup", "ürün grubu")
	aliasQty      = aliases("quantity", "qty", "stock", "in stock", "on hand", "count", "counted", "stock count", "stock quantity", "current quantity", "opening stock", "menge", "bestand", "anzahl", "lagerbestand", "istbestand", "miktar", "adet", "stok", "stok miktarı", "mevcut")
	aliasDesc     = aliases("description", "details", "beschreibung", "artikelbeschreibung", "açıklama")
	aliasTax      = aliases("tax", "tax rate", "tax %", "vat", "vat rate", "vat %", "mwst", "mwst-satz", "steuersatz", "steuer", "ust", "kdv", "kdv oranı", "kdv oran")
	aliasWeighed  = aliases("sold by weight", "weighed", "sold_by_weight", "nach gewicht", "gewichtsartikel", "tartılı ürün", "tartılı")
	aliasTrack    = aliases("track stock", "track inventory", "track_stock", "bestandsführung", "lagerführung", "stok takibi")

	aliasFullName  = aliases("name", "customer name", "customer", "full name", "kunde", "kundenname", "kunden name", "müşteri", "müşteri adı", "ad soyad", "adı soyadı", "isim")
	aliasFirstName = aliases("first name", "firstname", "given name", "vorname", "ad", "adı")
	aliasLastName  = aliases("last name", "lastname", "surname", "family name", "nachname", "familienname", "soyad", "soyadı")
	aliasEmail     = aliases("email", "e-mail", "email address", "mail", "e-posta", "eposta", "e-posta adresi")
	aliasPhone     = aliases("phone", "phone number", "mobile", "telephone", "tel", "telefon", "telefonnummer", "handy", "mobil", "cep", "telefon numarası", "cep telefonu")
	aliasPostcode  = aliases("postcode", "post code", "zip", "zip code", "postal code", "plz", "postleitzahl", "posta kodu")
	aliasAddress   = aliases("address", "address 1", "street", "street address", "straße", "strasse", "adresse", "adres", "sokak")
	aliasCity      = aliases("city", "town", "stadt", "ort", "şehir", "il")
	aliasCountry   = aliases("country", "land", "ülke")
)

// headerFolds are the folded forms a header can match an alias by: itself,
// without a trailing "(...)", and without a " [..]" suffix — the same
// leniency headerIndex gives the English synonyms.
func headerFolds(h string) []string {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
	out := []string{foldKey(lower)}
	if s := stripTrailingParen(lower); s != lower {
		out = append(out, foldKey(s))
	}
	if i := strings.Index(lower, " ["); i > 0 {
		out = append(out, foldKey(lower[:i]))
	}
	return out
}

// colFor returns the first column whose header matches set (-1 = none);
// skip lists columns already claimed.
func colFor(headers []string, set aliasSet, skip ...int) int {
	for i, h := range headers {
		if containsInt(skip, i) {
			continue
		}
		for _, f := range headerFolds(h) {
			if f != "" && set[f] {
				return i
			}
		}
	}
	return -1
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// foldedSet is the folded exact headers, for the source signatures.
func foldedSet(headers []string) map[string]bool {
	m := make(map[string]bool, len(headers))
	for _, h := range headers {
		m[foldKey(h)] = true
	}
	return m
}

// DetectSource names the source system of a header row (ADR-0168 §1):
// eposnow, my-export (our own my. export), then a grandfathered vendor
// format, then till-export (our till's own export), else generic /
// generic-erp as DetectFormat says.
func DetectSource(headers []string) string {
	f := foldedSet(headers)
	if f["salepriceinctax"] && f["salepriceextax"] && f["categoryid"] && f["isstocktracked"] {
		return SourceEposNow
	}
	// the my. export's keys are snake_case; no other source writes them
	exact := func(name string) bool { return hasExactHeader(headers, name) }
	if exact("sold_by_weight") && (exact("id") || exact("barcodes") || exact("track_stock") || exact("modifier_groups")) {
		return SourceMyExport
	}
	format := DetectFormat(headers)
	if format != FormatGeneric && format != FormatGenericERP {
		return format
	}
	if exact("sold by weight") && exact("in stock") && exact("active") &&
		(exact("item type") || exact("takeaway tax") || exact("net quantity") || exact("age restricted")) {
		return SourceTillExport
	}
	return format
}

// DetectKind decides what a file holds from its header row alone
// (ADR-0168 §2). products: a name and a price column (an Epos Now products
// export always); customers: a person-name column and one of email, phone,
// postcode, address, and no price; stock: SKU or barcode plus a quantity,
// and no price; customer columns together with a price are mixed_kinds;
// anything else is unknown_kind with the missing columns per kind.
func DetectKind(headers []string) KindDetection {
	p := profileOf(headers)
	d := KindDetection{MissingByKind: map[string][]string{
		KindProducts:  p.missing(KindProducts),
		KindCustomers: p.missing(KindCustomers),
		KindStock:     p.missing(KindStock),
	}}
	switch {
	case p.eposNow:
		d.Kind = KindProducts
	case p.person && p.contact && p.price:
		d.Refusal = RefusalMixedKinds
	case p.person && p.contact:
		d.Kind = KindCustomers
	case p.name && p.price:
		d.Kind = KindProducts
	case p.key && p.qty && !p.price:
		d.Kind = KindStock
	default:
		d.Refusal = RefusalUnknownKind
	}
	if d.Kind != "" {
		d.Matched = p.matched(d.Kind)
		d.MissingByKind = nil
	} else if d.Refusal == RefusalMixedKinds {
		d.Matched = p.matched("")
		d.MissingByKind = nil
	}
	return d
}

// profile is which neutral fields a header row carries.
type profile struct {
	eposNow, name, price, key, qty, person, contact          bool
	hasSKU, hasBarcode, hasEmail, hasPhone, hasPost, hasAddr bool
}

func profileOf(headers []string) profile {
	var p profile
	p.eposNow = DetectSource(headers) == SourceEposNow
	p.name = hasField(headers, "name", aliasName)
	// Shopify's grandfathered export prices per variant ("Variant Price"),
	// which ParseMapped reads; it is a products file (review B2).
	p.price = hasField(headers, "price", aliasPrice) || p.eposNow ||
		(DetectFormat(headers) == FormatShopify && hasExactHeader(headers, "variant price"))
	p.hasSKU = hasField(headers, "sku", aliasSKU)
	p.hasBarcode = hasField(headers, "barcode", aliasBarcode)
	p.key = p.hasSKU || p.hasBarcode
	p.qty = hasField(headers, "stock", aliasQty)
	p.hasEmail = colFor(headers, aliasEmail) >= 0
	p.hasPhone = colFor(headers, aliasPhone) >= 0
	p.hasPost = colFor(headers, aliasPostcode) >= 0
	p.hasAddr = colFor(headers, aliasAddress) >= 0
	p.person = colFor(headers, aliasFullName) >= 0 || colFor(headers, aliasFirstName) >= 0 || colFor(headers, aliasLastName) >= 0
	p.contact = p.hasEmail || p.hasPhone || p.hasPost || p.hasAddr
	if p.eposNow {
		p.name = true
	}
	return p
}

// hasField is the existing English synonym match OR a DE/TR alias.
func hasField(headers []string, field string, set aliasSet) bool {
	return hasColumn(headers, field) || colFor(headers, set) >= 0
}

func (p profile) missing(kind string) []string {
	var m []string
	switch kind {
	case KindProducts:
		if !p.name {
			m = append(m, "name")
		}
		if !p.price {
			m = append(m, "price")
		}
	case KindCustomers:
		if !p.person {
			m = append(m, "name")
		}
		if !p.contact {
			m = append(m, "email|phone|postcode|address")
		}
	case KindStock:
		if !p.key {
			m = append(m, "sku|barcode")
		}
		if !p.qty {
			m = append(m, "quantity")
		}
	}
	return m
}

func (p profile) matched(kind string) []string {
	var m []string
	add := func(ok bool, n string) {
		if ok {
			m = append(m, n)
		}
	}
	if kind == KindCustomers || kind == "" {
		add(p.person, "name")
		add(p.hasEmail, "email")
		add(p.hasPhone, "phone")
		add(p.hasPost, "postcode")
		add(p.hasAddr, "address")
		add(p.price && kind == "", "price")
		return m
	}
	add(p.name, "name")
	add(p.price, "price")
	add(p.hasSKU, "sku")
	add(p.hasBarcode, "barcode")
	add(p.qty, "quantity")
	return m
}

// readSmart reads the whole upload (bounded) and its header row.
func readSmart(r io.Reader) (buf []byte, headers []string, comma rune, err error) {
	buf, err = io.ReadAll(io.LimitReader(r, maxSmartFileBytes+1))
	if err != nil {
		return nil, nil, 0, err
	}
	if len(buf) > maxSmartFileBytes {
		return nil, nil, 0, ErrFileTooLarge
	}
	buf = bytes.TrimPrefix(buf, utf8BOM)
	head := buf
	if len(head) > HeaderRowMaxBytes {
		head = head[:HeaderRowMaxBytes]
	}
	comma = sniffDelimiter(head)
	cr := csv.NewReader(bytes.NewReader(buf))
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	headers, err = cr.Read()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("read header: %w", err)
	}
	return buf, headers, comma, nil
}

// gate refuses a file that is not the kind asked for. The detection's own
// refusal (mixed/unknown) wins unless the operator forced the kind.
func gate(headers []string, kind string, o Options) error {
	d := DetectKind(headers)
	if d.Refusal != "" && !o.Force {
		return &KindError{Code: d.Refusal, Detection: d}
	}
	if miss := profileOf(headers).missing(kind); len(miss) > 0 {
		return &KindError{Code: RefusalMissingColumns, Kind: kind, Missing: miss, Detection: d}
	}
	return nil
}
