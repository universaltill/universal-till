package catsync

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ut-docs#4485: every per-row issue carries a stable code plus args so the
// till UI can translate it; the English text the JSON API returns is
// unchanged.

func TestParseRow_IssueCodesAndEnglishTextUnchanged(t *testing.T) {
	long := strings.Repeat("x", 300)
	cases := []struct {
		name, raw, code, text string
		args                  []string
	}{
		{"not object", `not json`, IssueRowNotObject, "row is not a valid item object", nil},
		{"two objects", `{"external_id":"a"} {"external_id":"b"}`, IssueRowNotSingleObject, "row is not a single JSON object", nil},
		{"no id", `{"name":"x","price_minor":1}`, IssueExternalIDInvalid, "external_id is missing or invalid", nil},
		{"no name", `{"external_id":"a","price_minor":1}`, IssueNameInvalid, "name is missing or invalid", nil},
		{"no price", `{"external_id":"a","name":"x"}`, IssuePriceInvalid, "price_minor is missing or out of range", nil},
		{"bad sku", fmt.Sprintf(`{"external_id":"a","name":"x","price_minor":1,"sku":%q}`, long), IssueSKUInvalid, "sku is invalid", nil},
		{"deep", `{"external_id":"a","name":"x","price_minor":1,"category_path":["1","2","3","4","5"]}`, IssueCategoryTooDeep, "category_path is deeper than 4 levels", []string{"4"}},
		{"bad level", `{"external_id":"a","name":"x","price_minor":1,"category_path":[""]}`, IssueCategoryLevelInvalid, "category_path has an empty or invalid level", nil},
		{"vat", `{"external_id":"a","name":"x","price_minor":1,"vat_rate_bp":10001}`, IssueVATOutOfRange, "vat_rate_bp is out of range (0-10000)", nil},
		{"stock", `{"external_id":"a","name":"x","price_minor":1,"stock_qty":1e12}`, IssueStockOutOfRange, "stock_qty is out of range", nil},
		{"many codes", `{"external_id":"a","name":"x","price_minor":1,"barcodes":["1","2","3","4","5","6","7","8","9","10","11"]}`, IssueTooManyBarcodes, "more than 10 barcodes", []string{"10"}},
		{"bad code", `{"external_id":"a","name":"x","price_minor":1,"barcodes":["no spaces"]}`, IssueBarcodeInvalid, "a barcode is invalid", nil},
	}
	for _, c := range cases {
		_, _, is := parseRow(json.RawMessage(c.raw))
		if is.Code != c.code || is.Text() != c.text || strings.Join(is.Args, "|") != strings.Join(c.args, "|") {
			t.Errorf("%s: got code=%q args=%v text=%q, want code=%q args=%v text=%q", c.name, is.Code, is.Args, is.Text(), c.code, c.args, c.text)
		}
	}
}

func TestRun_IssueSamplesCarryCodeArgsAndEnglish(t *testing.T) {
	sqlDB := openDB(t)
	run(t, sqlDB, true, coffee)
	seedItem(t, sqlDB, "Other", "OTHER", "4006381333931")
	res := run(t, sqlDB, true,
		`{"external_id":"d1","name":"A","price_minor":1}`,
		`{"external_id":"d1","name":"B","price_minor":1}`,
		strings.Replace(coffee, `["5000000000017"]`, `["5000000000017","4006381333931"]`, 1),
		`{"external_id":"d2","name":"C","price_minor":1,"barcodes":["7777777777"]}`,
		`{"external_id":"d3","name":"D","price_minor":1,"barcodes":["7777777777"]}`,
	)
	byRow := map[int]Sample{}
	for _, s := range res.Samples {
		byRow[s.Row] = s
	}
	want := map[int]struct{ code, text string }{
		2: {IssueExternalIDDuplicate, "external_id appears on more than one row"},
		3: {IssueBarcodeTaken, "barcode 4006381333931 belongs to another item"},
		5: {IssueBarcodeDuplicate, "barcode 7777777777 appears on more than one row"},
	}
	for row, w := range want {
		s := byRow[row]
		if s.Action != ActionIssue || s.IssueCode != w.code || s.Issue != w.text {
			t.Errorf("row %d: %+v, want code %q text %q", row, s, w.code, w.text)
		}
	}
	if s := byRow[3]; len(s.IssueArgs) != 1 || s.IssueArgs[0] != "4006381333931" {
		t.Errorf("row 3 args = %v", s.IssueArgs)
	}
	for _, s := range res.Samples {
		if s.Action == ActionIssue && (s.IssueCode == "" || s.Issue == "") {
			t.Errorf("issue sample without code/text: %+v", s)
		}
		if s.Action != ActionIssue && (s.IssueCode != "" || s.IssueArgs != nil) {
			t.Errorf("non-issue sample carries an issue: %+v", s)
		}
	}
}

func TestRun_AlreadyLinkedAndApplyFailedCodes(t *testing.T) {
	sqlDB := openDB(t)
	run(t, sqlDB, true, coffee)
	// Same barcode, different source record: the item is linked elsewhere.
	other := strings.Replace(coffee, `"external_id":"p-1"`, `"external_id":"p-9"`, 1)
	s := run(t, sqlDB, true, other).Samples[0]
	if s.IssueCode != IssueItemLinkedElsewhere || s.Issue != "the matching item is already linked to source record p-1" || len(s.IssueArgs) != 1 || s.IssueArgs[0] != "p-1" {
		t.Errorf("linked elsewhere: %+v", s)
	}

	defer func(h func(string) error) { applyFault = h }(applyFault)
	applyFault = func(string) error { return fmt.Errorf("injected apply failure") }
	s = run(t, sqlDB, true, `{"external_id":"n-1","name":"New","price_minor":5}`).Samples[0]
	if s.IssueCode != IssueApplyFailed || s.Issue != "could not apply this row" {
		t.Errorf("apply failed: %+v", s)
	}
}

func TestIssueCodes_AllHaveEnglishFormat(t *testing.T) {
	codes := IssueCodes
	if len(codes) < 21 {
		t.Fatalf("only %d codes", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] || c == "" {
			t.Errorf("duplicate or empty code %q", c)
		}
		seen[c] = true
		if strings.TrimSpace(issueText[c]) == "" {
			t.Errorf("code %q has no English text", c)
		}
	}
}
