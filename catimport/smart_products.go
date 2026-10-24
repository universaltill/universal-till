package catimport

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ParseProducts reads a products file of any source (ADR-0168): the Epos
// Now shape is parsed here; every other source goes through ParseMapped
// with the DE/TR aliases added as an automatic column mapping (the shop's
// own Options.Mapping wins), then the till-export / my-export extras. The
// file is refused whole (*KindError) when it is not a products file;
// otherwise Result.Issues carries the row issues with their suggestions.
func ParseProducts(r io.Reader, o Options) (Result, error) {
	buf, headers, comma, err := readSmart(r)
	if err != nil {
		return Result{}, err
	}
	if err := gate(headers, KindProducts, o); err != nil {
		return Result{}, err
	}
	source := DetectSource(headers)
	var res Result
	var pre []RowIssue
	if source == SourceEposNow {
		res, pre, err = parseEposNow(buf, comma, headers, o)
		pre = append(pre, mapItemIssues(res.Items)...)
	} else {
		res, err = parseGenericProducts(buf, headers, source, o)
		if err == nil {
			pre = mapItemIssues(res.Items)
		}
	}
	if err != nil {
		return res, err
	}
	res.Source, res.Kind = source, KindProducts
	res.Issues = finishProducts(&res, pre, o)
	return res, nil
}

// parseGenericProducts is ParseMapped plus the locale aliases and the
// extras only our own exports carry.
func parseGenericProducts(buf []byte, headers []string, source string, o Options) (Result, error) {
	m := o.Mapping
	if IsGenericFormat(DetectFormat(headers)) {
		m = withAliasMapping(headers, o.Mapping)
	}
	res, err := ParseMapped(bytes.NewReader(buf), m, o.CurrencyDecimals, o.EnabledSymbologyIDs, o.UseItemNumbersAsBarcodes)
	if err != nil {
		return res, err
	}
	if source == SourceTillExport || source == SourceMyExport {
		readExportExtras(buf, headers, source, o, &res)
	}
	return res, nil
}

// withAliasMapping adds, for each neutral field the English synonyms left
// unmatched, the one header a DE/TR alias names. A field or a header the
// shop's mapping already mentions is left alone; an ambiguous header (two
// columns folding the same) is skipped, never guessed.
func withAliasMapping(headers []string, user Mapping) Mapping {
	idx := headerIndex(headers)
	claimed := map[int]bool{}
	for _, c := range idx {
		claimed[c] = true
	}
	userHeaders := map[string]bool{}
	for _, h := range user {
		userHeaders[foldHeader(h)] = true
	}
	out := Mapping{}
	for f, h := range user {
		out[f] = h
	}
	fields := []struct {
		field string
		set   aliasSet
	}{
		{"name", aliasName}, {"price", aliasPrice}, {"sku", aliasSKU}, {"barcode", aliasBarcode},
		{"category", aliasCategory}, {"description", aliasDesc}, {"stock", aliasQty}, {"tax", aliasTax},
		{"weighed", aliasWeighed}, {"track_stock", aliasTrack},
	}
	for _, f := range fields {
		if _, ok := idx[f.field]; ok {
			continue
		}
		if _, ok := user[f.field]; ok {
			continue
		}
		// skip grows monotonically, so the walk always ends (review B1: a
		// skip list that dropped the previous column looped forever when two
		// alias columns were both mapped elsewhere by the shop).
		skip := claimedCols(claimed)
		col := colFor(headers, f.set, skip...)
		for col >= 0 && userHeaders[foldHeader(headers[col])] {
			skip = append(skip, col)
			col = colFor(headers, f.set, skip...)
		}
		if col < 0 || dupHeader(headers, col) {
			continue
		}
		out[f.field] = headers[col]
		claimed[col] = true
	}
	if len(out) == 0 {
		return user
	}
	return out
}

func claimedCols(m map[int]bool) []int {
	var out []int
	for c := range m {
		out = append(out, c)
	}
	return out
}

func dupHeader(headers []string, col int) bool {
	k := foldHeader(strings.TrimPrefix(headers[col], "\ufeff"))
	n := 0
	for _, h := range headers {
		if foldHeader(strings.TrimPrefix(h, "\ufeff")) == k {
			n++
		}
	}
	return n > 1
}

// readExportExtras re-reads our own exports for what ParseMapped does not
// carry: the Active column and, for a my. export, the first of the
// ";"-joined barcodes.
func readExportExtras(buf []byte, headers []string, source string, o Options, res *Result) {
	active := colFor(headers, aliases("active"))
	barcodes, idCol := -1, -1
	if source == SourceMyExport {
		barcodes = colFor(headers, aliases("barcodes"))
		// the my. export writes a lowercase "id" (ADR-0168 §3); match it exactly
		for i, h := range headers {
			if strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF"))) == "id" {
				idCol = i
				break
			}
		}
	}
	if active < 0 && barcodes < 0 && idCol < 0 {
		return
	}
	cr := csv.NewReader(bytes.NewReader(buf))
	cr.Comma = sniffDelimiter(buf[:min(len(buf), HeaderRowMaxBytes)])
	cr.FieldsPerRecord = -1
	if _, err := cr.Read(); err != nil {
		return
	}
	n := 0
	for {
		rec, err := cr.Read()
		if err != nil {
			return
		}
		if n >= len(res.Items) {
			return
		}
		it := &res.Items[n]
		n++
		if active >= 0 && active < len(rec) {
			if v, ok := parseBoolLoose(rec[active]); ok {
				it.Active, it.HasActive = v.val, true
			}
		}
		if idCol >= 0 && idCol < len(rec) {
			// kept only when it fits maxItemIDLen and holds no control
			// character or whitespace; anything else is no id at all
			id := StripCSVDefuse(strings.TrimSpace(rec[idCol]))
			if len(id) > maxItemIDLen || strings.ContainsFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
				id = ""
			}
			it.ItemID = id
		}
		if barcodes >= 0 && barcodes < len(rec) {
			first, _, _ := strings.Cut(rec[barcodes], ";")
			first = StripCSVDefuse(strings.TrimSpace(first))
			it.Barcode, it.BarcodeType, it.BarcodeIssue, it.BarcodeIssueRaw = "", "", "", ""
			if first != "" {
				if dec, ok := normalizeBarcode(first, o.EnabledSymbologyIDs); ok {
					it.Barcode, it.BarcodeType = dec.LookupKey, dec.SymbologyID
				} else {
					it.BarcodeIssue, it.BarcodeIssueRaw = BarcodeIssueNoSymbologyMatch, first
				}
			}
		}
	}
}

// maxItemIDLen bounds the my. export id carried in ImportItem.ItemID.
const maxItemIDLen = 64

// boolGuess is a parsed yes/no cell: exact when the cell is a recognised
// yes/no word, otherwise a best guess from its first letter.
type boolGuess struct {
	val   bool
	exact bool
}

// parseBoolLoose reads a yes/no cell in EN, DE or TR. ok is false for a
// cell with no reading at all.
func parseBoolLoose(raw string) (boolGuess, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return boolGuess{}, false
	}
	switch v {
	case "y", "yes", "true", "1", "t", "j", "ja", "e", "evet", "x":
		return boolGuess{true, true}, true
	case "n", "no", "false", "0", "f", "nein", "h", "hayır", "hayir":
		return boolGuess{false, true}, true
	}
	switch v[0] {
	case 'y', 'j', 't', 'e':
		return boolGuess{true, false}, true
	case 'n', 'f', 'h':
		return boolGuess{false, false}, true
	}
	return boolGuess{}, false
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// mapItemIssues turns the flags an older parse put on its items into row
// issues, with suggestions where the rule knows one.
func mapItemIssues(items []ImportItem) []RowIssue {
	var out []RowIssue
	for _, it := range items {
		row := it.Row
		switch it.Issue {
		case IssueMissingName, IssueMissingNameAndBadPrice:
			out = append(out, RowIssue{Row: row, Code: IssueCodeMissingName, Severity: SeverityError, Field: "name"})
		}
		if it.Issue == IssueBadPrice || it.Issue == IssueMissingNameAndBadPrice {
			out = append(out, priceIssue(row, it.IssueDetail, "price"))
		}
		if it.BarcodeIssue == BarcodeIssueNoSymbologyMatch {
			out = append(out, RowIssue{Row: row, Code: IssueCodeNoSymbology, Severity: SeverityWarning, Field: "barcode", Original: it.BarcodeIssueRaw, Suggested: sp("")})
		}
		if it.TaxIssue != "" {
			out = append(out, RowIssue{Row: row, Code: IssueCodeBadTax, Severity: SeverityWarning, Field: "tax", Original: it.TaxIssueRaw})
		}
		if it.TakeawayTaxIssue != "" {
			out = append(out, RowIssue{Row: row, Code: IssueCodeBadTax, Severity: SeverityWarning, Field: "takeaway_tax", Original: it.TakeawayTaxIssueRaw})
		}
		if it.SKUIssue == SKUIssueDuplicateInFile {
			out = append(out, RowIssue{Row: row, Code: IssueCodeDuplicateSKU, Severity: SeverityWarning, Field: "sku", Original: it.SKUIssueRaw})
		}
	}
	return out
}

// priceIssue is a price cell that did not parse: blank = missing_price
// (blocking, nothing to suggest), else bad_price with the best guess.
func priceIssue(row int, raw, field string) RowIssue {
	if strings.TrimSpace(raw) == "" {
		return RowIssue{Row: row, Code: IssueCodeMissingPrice, Severity: SeverityError, Field: field}
	}
	is := RowIssue{Row: row, Code: IssueCodeBadPrice, Severity: SeverityError, Field: field, Original: raw}
	if g, ok := guessNumber(raw); ok {
		is.Suggested = sp(g)
	}
	return is
}

// guessNumber is the best reading of a number cell ParsePrice refused: a
// sign is dropped, junk around the digits is stripped. ok only when the
// guess itself parses as a price.
func guessNumber(raw string) (string, bool) {
	g := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' || r == ',' {
			return r
		}
		return -1
	}, raw)
	if g == "" || g == raw {
		return "", false
	}
	if _, err := ParsePrice(g, 2); err != nil {
		return "", false
	}
	return g, true
}

// parseEposNow reads the Epos Now products export. CategoryId holds the
// category NAME. Price = SalePriceIncTax; the rate is derived from inc/ex
// and snapped to the store's rates, the group id (e.g. "20VAT") breaking
// ties. SKU = ArticleCode only; a row without one stays without and
// finishProducts suggests one. A duplicate barcode or name never drops a
// row.
func parseEposNow(buf []byte, comma rune, headers []string, o Options) (Result, []RowIssue, error) {
	col := map[string]int{}
	for i, h := range headers {
		k := foldKey(h)
		if _, dup := col[k]; !dup {
			col[k] = i
		}
	}
	cr := csv.NewReader(bytes.NewReader(buf))
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	if _, err := cr.Read(); err != nil {
		return Result{}, nil, fmt.Errorf("read header: %w", err)
	}
	res := Result{Format: FormatEposNow}
	var issues []RowIssue
	row := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		row++
		if err != nil {
			return res, nil, fmt.Errorf("row %d: %w", row, err)
		}
		get := func(name string) string {
			i, ok := col[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		add := func(code, sev, field, orig string, sug *string) {
			issues = append(issues, RowIssue{Row: row, Code: code, Severity: sev, Field: field, Original: orig, Suggested: sug})
		}
		it := ImportItem{Row: row, ItemType: "standard"}
		it.Name = StripCSVDefuse(get("name"))
		it.Category = StripCSVDefuse(get("categoryid"))
		it.Description = StripCSVDefuse(get("description"))
		it.SKU = StripCSVDefuse(get("articlecode"))
		it.Brand = StripCSVDefuse(get("brandname"))
		it.Supplier = StripCSVDefuse(get("suppliername"))
		it.ImageURL = get("imageurl")
		it.TaxGroup = get("salepricetaxgroupid")

		if raw := StripCSVDefuse(get("barcode")); raw != "" {
			if dec, ok := normalizeBarcode(raw, o.EnabledSymbologyIDs); ok {
				it.Barcode, it.BarcodeType = dec.LookupKey, dec.SymbologyID
			} else {
				it.BarcodeIssue, it.BarcodeIssueRaw = BarcodeIssueNoSymbologyMatch, raw
			}
		}

		// price: SalePriceIncTax. Blank = missing (blocking unless the item
		// is priced at sale, below), bad = blocking.
		incRaw, exRaw := get("salepriceinctax"), get("salepriceextax")
		var inc, ex int64
		incOK, exOK := false, false
		if incRaw != "" {
			if v, err := ParsePrice(incRaw, o.CurrencyDecimals); err == nil {
				inc, incOK = v, true
				it.PriceMinor = v
			} else {
				it.Issue, it.IssueDetail = IssueBadPrice, incRaw
			}
		}
		if exRaw != "" {
			if v, err := ParsePrice(exRaw, o.CurrencyDecimals); err == nil {
				ex, exOK = v, true
				it.PriceExTaxMinor = v
			} else {
				add(IssueCodeBadNumber, SeverityWarning, "price_ex_tax", exRaw, nil)
			}
		}
		if it.Name == "" {
			if it.Issue == IssueBadPrice {
				it.Issue = IssueMissingNameAndBadPrice
			} else {
				it.Issue = IssueMissingName
			}
		}

		// cost: ex-tax preferred, inc-tax otherwise; compare-at = RRPrice
		for _, c := range []string{"costpriceextax", "costpriceinctax"} {
			if raw := get(c); raw != "" && !it.HasCost {
				if v, err := ParsePrice(raw, o.CurrencyDecimals); err == nil {
					it.CostMinor, it.HasCost = v, true
				} else {
					add(IssueCodeBadNumber, SeverityWarning, "cost_price", raw, nil)
				}
			}
		}
		if raw := get("rrprice"); raw != "" {
			if v, err := ParsePrice(raw, o.CurrencyDecimals); err == nil && v > 0 {
				it.CompareAtMinor, it.HasCompareAt = v, true
			} else if err != nil {
				add(IssueCodeBadNumber, SeverityWarning, "compare_at_price", raw, nil)
			}
		}

		// yes/no columns: an unrecognised word is flagged with its best guess
		boolCol := func(name, field string) (val, has bool) {
			raw := get(name)
			if raw == "" {
				return false, false
			}
			g, ok := parseBoolLoose(raw)
			if !ok {
				add(IssueCodeBadBoolean, SeverityWarning, field, raw, nil)
				return false, false
			}
			if !g.exact {
				add(IssueCodeBadBoolean, SeverityWarning, field, raw, sp(yesNo(g.val)))
			}
			return g.val, true
		}
		it.TracksStock, it.HasTracksStock = boolCol("isstocktracked", "track_stock")
		it.Active, it.HasActive = boolCol("sellontill", "active")
		it.VariablePrice, _ = boolCol("isvariableprice", "variable_price")
		// A blank price is an error, never a free item (review B3); a
		// variable-price item is priced at sale, so blank is no issue.
		if incRaw == "" && !it.VariablePrice {
			add(IssueCodeMissingPrice, SeverityError, "price", "", nil)
		}

		// tax: derived from inc/ex, snapped to the store's rates
		bp, has, iss := eposTax(ex, exOK, inc, incOK, it.TaxGroup, o.KnownTaxRatesBP)
		it.TaxRateBP, it.HasTax = bp, has
		if iss != nil {
			iss.Row = row
			issues = append(issues, *iss)
		}
		res.Items = append(res.Items, it)
	}
	return res, issues, nil
}

// groupRateBP reads the rate a tax group id starts with ("20VAT" -> 2000,
// "7.5VAT" -> 750); ok is false when it starts with no number.
func groupRateBP(group string) (int, bool) {
	end := 0
	for end < len(group) && (group[end] >= '0' && group[end] <= '9' || group[end] == '.') {
		end++
	}
	if end == 0 {
		return 0, false
	}
	f, err := strconv.ParseFloat(group[:end], 64)
	if err != nil || f < 0 || f > 100 {
		return 0, false
	}
	return int(math.Round(f * 100)), true
}

func percentString(bp int) string {
	return strconv.FormatFloat(float64(bp)/100, 'f', -1, 64)
}

func nearestRate(known []int, bp int) int {
	best := known[0]
	for _, k := range known[1:] {
		if abs(k-bp) < abs(best-bp) {
			best = k
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// eposTax decides the row's rate. Known rates are the store's: an
// inc/ex pair must fit one of them to within a minor unit (rounding); the
// group id breaks a tie between rates that both fit. A rate fitting none
// is unknown_tax_rate (nearest rate suggested), two fitting is
// ambiguous_vat; neither is ever guessed into the item. A zero price has
// no ratio, so the group id alone speaks. Without known rates the group
// id, else the derived rate rounded to half a percent, is used as it reads.
func eposTax(ex int64, exOK bool, inc int64, incOK bool, group string, known []int) (int, bool, *RowIssue) {
	hint, hintOK := groupRateBP(group)
	paired := exOK && incOK && ex > 0 && inc > 0
	derived := 0
	if paired {
		derived = int(math.Round(float64(inc-ex) * 10000 / float64(ex)))
	}
	if len(known) == 0 {
		switch {
		case hintOK:
			return hint, true, nil
		case paired:
			return int(math.Round(float64(derived)/50)) * 50, true, nil
		}
		return 0, false, nil
	}
	contains := func(bp int) bool {
		for _, k := range known {
			if k == bp {
				return true
			}
		}
		return false
	}
	unknown := func(orig string, near int) (int, bool, *RowIssue) {
		return 0, false, &RowIssue{Code: IssueCodeUnknownTaxRate, Severity: SeverityWarning, Field: "tax", Original: orig, Suggested: sp(percentString(nearestRate(known, near)))}
	}
	if !paired {
		switch {
		case !hintOK:
			return 0, false, nil
		case contains(hint):
			return hint, true, nil
		}
		return unknown(group, hint)
	}
	var fits []int
	for _, k := range known {
		want := (ex*int64(10000+k) + 5000) / 10000
		if d := want - inc; d >= -1 && d <= 1 {
			fits = append(fits, k)
		}
	}
	switch {
	case hintOK && containsInt(fits, hint):
		return hint, true, nil
	case len(fits) == 1:
		return fits[0], true, nil
	case len(fits) > 1:
		orig := group
		if orig == "" {
			orig = percentString(derived)
		}
		return 0, false, &RowIssue{Code: IssueCodeAmbiguousVAT, Severity: SeverityWarning, Field: "tax", Original: orig, Suggested: sp(percentString(nearestRate(fits, derived)))}
	}
	orig := group
	if orig == "" {
		orig = percentString(derived)
	}
	return unknown(orig, derived)
}

// finishProducts runs the checks every products source shares and returns
// all issues ordered by row: a duplicate barcode (first row keeps it, the
// repeat is cleared), a missing SKU or barcode (with a generated value to
// accept), an over-long name, an unknown category, a missing or zero
// price. pre are the issues the source's own parse raised.
func finishProducts(res *Result, pre []RowIssue, o Options) []RowIssue {
	issues := append([]RowIssue(nil), pre...)
	// had[row,code]: O(1) "this row already has that issue" (review M2:
	// scanning every issue per row was quadratic in the file's rows).
	type rowCode struct {
		row  int
		code string
	}
	had := map[rowCode]bool{}
	for _, is := range pre {
		had[rowCode{is.Row, is.Code}] = true
	}
	known := map[string]bool{}
	for _, c := range o.KnownCategories {
		known[strings.ToLower(strings.TrimSpace(c))] = true
	}
	takenSKU := map[string]bool{}
	for _, it := range res.Items {
		if it.SKU != "" {
			takenSKU[strings.ToLower(it.SKU)] = true
		}
	}
	firstBarcode := map[string]int{}
	for i := range res.Items {
		it := &res.Items[i]
		row := it.Row
		if row == 0 {
			row = i + 2
			it.Row = row
		}
		add := func(code, sev, field, orig string, sug *string) {
			issues = append(issues, RowIssue{Row: row, Code: code, Severity: sev, Field: field, Original: orig, Suggested: sug})
			had[rowCode{row, code}] = true
		}
		if it.Issue != "" && it.Issue != IssueBadPrice && it.Issue != IssueMissingName && it.Issue != IssueMissingNameAndBadPrice {
			continue // a source-specific blocking issue is mapped by the source
		}
		if it.Barcode != "" {
			if first, dup := firstBarcode[it.Barcode]; dup && first != row {
				add(IssueCodeDuplicateBarcode, SeverityWarning, "barcode", it.Barcode, sp(""))
				it.Barcode, it.BarcodeType = "", ""
			} else {
				firstBarcode[it.Barcode] = row
			}
		}
		if it.Name != "" {
			if utf8.RuneCountInString(it.Name) > NameMaxRunes {
				add(IssueCodeNameTooLong, SeverityWarning, "name", it.Name, sp(shorten(it.Name, NameMaxRunes)))
			}
			if it.SKU == "" {
				sku := suggestSKU(it.Name, row, takenSKU)
				add(IssueCodeMissingSKU, SeverityWarning, "sku", "", sp(sku))
			}
		}
		if it.Barcode == "" && it.BarcodeIssue == "" && !had[rowCode{row, IssueCodeDuplicateBarcode}] {
			is := RowIssue{Row: row, Code: IssueCodeMissingBarcode, Severity: SeverityWarning, Field: "barcode"}
			if it.SKU != "" {
				if code, _, iss, _ := DeriveNumberBarcode(it.SKU, o.EnabledSymbologyIDs); iss == "" && code != "" {
					is.Suggested = sp(code)
				}
			}
			issues = append(issues, is)
			had[rowCode{row, is.Code}] = true
		}
		if o.KnownCategories != nil && strings.TrimSpace(it.Category) != "" && !known[strings.ToLower(strings.TrimSpace(it.Category))] {
			add(IssueCodeUnknownCategory, SeverityWarning, "category", it.Category, sp(it.Category))
		}
		if it.Issue == "" && it.PriceMinor == 0 && !it.VariablePrice && !had[rowCode{row, IssueCodeMissingPrice}] {
			add(IssueCodeZeroPrice, SeverityWarning, "price", "0", nil)
		}
	}
	sort.SliceStable(issues, func(a, b int) bool { return issues[a].Row < issues[b].Row })
	return issues
}

// shorten cuts s to at most max runes on a word boundary where one is near
// the end, marking the cut with an ellipsis.
func shorten(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	cut := r[:max-1]
	for i := len(cut) - 1; i > max/2; i-- {
		if unicode.IsSpace(cut[i]) {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRightFunc(string(cut), unicode.IsSpace) + "…"
}

// suggestSKU is a generated SKU for a row that has none: the name's first
// three letters or digits plus the row number, made unique within the
// file. It is a suggestion; the consumer generates the real one in the
// till's own format when the item is created (#1459).
func suggestSKU(name string, row int, taken map[string]bool) string {
	var p []rune
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			p = append(p, unicode.ToUpper(r))
			if len(p) == 3 {
				break
			}
		}
	}
	prefix := string(p)
	if prefix == "" {
		prefix = "ITM"
	}
	sku := fmt.Sprintf("%s-%04d", prefix, row)
	for n := 2; taken[strings.ToLower(sku)]; n++ {
		sku = fmt.Sprintf("%s-%04d-%d", prefix, row, n)
	}
	taken[strings.ToLower(sku)] = true
	return sku
}
