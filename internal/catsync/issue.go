package catsync

import (
	"fmt"
	"maps"
	"slices"
)

// Per-row issue codes (ut-docs#4485). A code is stable API: the till
// translates "import.issue.<code>" with Args as the %s values, and a JSON
// client gets the code in Sample.IssueCode. Sample.Issue keeps the English
// text below, byte-for-byte what the API always returned.
const (
	IssueRowNotObject         = "row_not_object"
	IssueRowNotSingleObject   = "row_not_single_object"
	IssueExternalIDInvalid    = "external_id_invalid"
	IssueNameInvalid          = "name_invalid"
	IssuePriceInvalid         = "price_invalid"
	IssueSKUInvalid           = "sku_invalid"
	IssueCategoryTooDeep      = "category_too_deep"      // args: max depth
	IssueCategoryLevelInvalid = "category_level_invalid" //
	IssueVATOutOfRange        = "vat_out_of_range"
	IssueStockOutOfRange      = "stock_out_of_range"
	IssueTooManyBarcodes      = "too_many_barcodes" // args: max barcodes
	IssueBarcodeInvalid       = "barcode_invalid"
	IssueBarcodeNotAccepted   = "barcode_not_accepted" // args: barcode
	IssueDescriptionInvalid   = "description_invalid"
	IssueNetQuantityInvalid   = "net_quantity_invalid"
	IssueExternalIDDuplicate  = "external_id_duplicate"
	IssueBarcodeDuplicate     = "barcode_duplicate" // args: barcode
	IssueSKUDuplicate         = "sku_duplicate"
	IssueItemLinkedElsewhere  = "item_linked_elsewhere" // args: source record id
	IssueItemAlreadyMatched   = "item_already_matched"
	IssueBarcodeTaken         = "barcode_taken" // args: barcode
	IssueSKUTaken             = "sku_taken"
	IssueApplyFailed          = "apply_failed"
)

// issueText is the one code -> English format table; %s verbs take Args in
// order.
var issueText = map[string]string{
	IssueRowNotObject:         "row is not a valid item object",
	IssueRowNotSingleObject:   "row is not a single JSON object",
	IssueExternalIDInvalid:    "external_id is missing or invalid",
	IssueNameInvalid:          "name is missing or invalid",
	IssuePriceInvalid:         "price_minor is missing or out of range",
	IssueSKUInvalid:           "sku is invalid",
	IssueCategoryTooDeep:      "category_path is deeper than %s levels",
	IssueCategoryLevelInvalid: "category_path has an empty or invalid level",
	IssueVATOutOfRange:        "vat_rate_bp is out of range (0-10000)",
	IssueStockOutOfRange:      "stock_qty is out of range",
	IssueTooManyBarcodes:      "more than %s barcodes",
	IssueBarcodeInvalid:       "a barcode is invalid",
	IssueBarcodeNotAccepted:   "barcode %s is not accepted by this till's barcode settings",
	IssueDescriptionInvalid:   "description is too long or invalid",
	IssueNetQuantityInvalid:   "net_quantity_value and net_quantity_unit are invalid",
	IssueExternalIDDuplicate:  "external_id appears on more than one row",
	IssueBarcodeDuplicate:     "barcode %s appears on more than one row",
	IssueSKUDuplicate:         "sku appears on more than one row",
	IssueItemLinkedElsewhere:  "the matching item is already linked to source record %s",
	IssueItemAlreadyMatched:   "another row in this run already matched the same item",
	IssueBarcodeTaken:         "barcode %s belongs to another item",
	IssueSKUTaken:             "sku belongs to another item",
	IssueApplyFailed:          "could not apply this row",
}

// IssueCodes lists every issue code, sorted. A variable, not a func: only
// tests read it, and the deadcode guard flags a test-only func.
var IssueCodes = slices.Sorted(maps.Keys(issueText))

// rowIssue is one row's problem: a code plus the values its text names.
// The zero value means no issue.
type rowIssue struct {
	Code string
	Args []string
}

func newIssue(code string, args ...string) rowIssue { return rowIssue{Code: code, Args: args} }

func (i rowIssue) none() bool { return i.Code == "" }

// Text is the English text the JSON API returns.
func (i rowIssue) Text() string {
	args := make([]any, len(i.Args))
	for n, a := range i.Args {
		args[n] = a
	}
	return fmt.Sprintf(issueText[i.Code], args...)
}
