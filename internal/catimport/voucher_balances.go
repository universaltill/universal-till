package catimport

// ParseVoucherBalances (ut-docs#1834): parses a migrating merchant's opening
// voucher-liability export — one row per physical voucher card the merchant
// already has in circulation on their PREVIOUS system, each with a real
// outstanding balance that must land in THIS till's voucher-liability system
// (ut-docs#1008) as an opening balance, not a sale. Same header-detected,
// synonym-tolerant CSV convention as the rest of this package (see
// catimport.go's package doc and headerIndex), deliberately much simpler:
// two required columns (code, balance) plus one optional one (a holder
// label), no format auto-detection, no barcode/stock/tax machinery at all.
// Pure parsing, no DB — the pages layer (internal/pages/import_vouchers_page.go)
// previews and commits, same split as catimport.go's own header comment
// describes for the catalog importer.
import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNoCodeColumn is ParseVoucherBalances' own reason code (mirrors
// ErrNoNameColumn, ut-docs#303) for the single most common whole-file
// failure — a wrong-format upload with no recognisable "code" column — so
// the pages layer can show a translated message instead of this package's
// English text.
var ErrNoCodeColumn = errors.New("no code column recognised — is this a voucher balance export?")

// VoucherBalanceItem is one row that parsed cleanly: an opening liability
// ready to become a vouchers row plus its issuing voucher_transactions row
// (see internal/data/voucher_repo.go's CreateVoucher/RecordVoucherTransaction).
type VoucherBalanceItem struct {
	RowNum       int    // 1-based file line: the header is row 1, so the first data row is row 2 (same convention Parse's own row-error messages use)
	Code         string // becomes the voucher id (vouchers.id, operator-supplied TEXT PRIMARY KEY)
	BalanceMinor int64
	HolderLabel  string
}

// VoucherBalanceRowIssue is one row that did NOT parse cleanly — Code is
// best-effort (whatever the code column held, even if that's what made the
// row bad, e.g. a duplicate) and may be empty (a genuinely missing code).
type VoucherBalanceRowIssue struct {
	RowNum int
	Code   string
	Reason string // one of the VoucherBalanceIssue* consts below
}

// VoucherBalanceResult mirrors this package's existing Result/ImportResult
// shape: clean rows ready to import, plus every rejected row with a
// machine-readable reason the pages layer translates (this package has no
// locale of its own — see catimport.go's Issue* doc comment).
type VoucherBalanceResult struct {
	Items  []VoucherBalanceItem
	Issues []VoucherBalanceRowIssue
}

// VoucherBalanceIssue* reason codes. A row failing multiple checks reports
// the most specific single reason, in this fixed priority order
// (missing_code, then bad_balance, then non_positive_balance, then — only
// for rows that otherwise parsed cleanly — duplicate_code_in_file): this is
// a much simpler two-column format than the catalog importer, so
// ut-docs#1713's combined-reason machinery (IssueMissingNameAndBadPrice)
// isn't needed here.
const (
	VoucherBalanceIssueMissingCode = "missing_code"
	VoucherBalanceIssueBadBalance  = "bad_balance"
	// VoucherBalanceIssueZeroOrNegBalance: a real outstanding voucher
	// balance is always > 0 by definition — a zero or negative "balance"
	// in the source export is not a liability to import (data.CreateVoucher
	// itself rejects OriginalAmountMinor <= 0 too; this catches it earlier,
	// with a reason the operator can actually read).
	VoucherBalanceIssueZeroOrNegBalance = "non_positive_balance"
	// VoucherBalanceIssueDuplicateCodeInFile: two rows in THIS file claim
	// the same code. Unlike ParseBkp's same-PLU handling (catimport.go's
	// IssueDuplicateSKUInFile, which synthesizes a suffixed id so a real
	// product is never silently dropped), a voucher CODE is the physical
	// card's own printed identifier — inventing "V001-2" would create a
	// voucher balance no physical card actually carries. So neither
	// occurrence imports: both are reported, and the operator fixes the
	// source file and re-uploads, same as any other rejected row.
	VoucherBalanceIssueDuplicateCodeInFile = "duplicate_code_in_file"
)

var voucherBalanceColumnSynonyms = map[string][]string{
	"code":    {"code", "voucher code", "voucher id", "voucher_id", "id", "card code", "card number", "gutschein", "gutscheincode"},
	"balance": {"balance", "amount", "remaining", "remaining balance", "value", "outstanding"},
	"label":   {"label", "holder", "holder_label", "holder label", "customer", "name"},
}

// voucherBalanceHeaderIndex is this file's own header-matching pass — a
// smaller version of catimport.go's headerIndex (case-insensitive exact
// synonym match only; this format has no parenthetical-qualifier headers in
// the wild worth the two-pass paren-stripped fallback that function needs).
func voucherBalanceHeaderIndex(headers []string) map[string]int {
	idx := map[string]int{}
	for i, h := range headers {
		key := strings.ToLower(strings.TrimSpace(h))
		for field, syns := range voucherBalanceColumnSynonyms {
			if _, taken := idx[field]; taken {
				continue
			}
			for _, s := range syns {
				if key == s {
					idx[field] = i
					break
				}
			}
		}
	}
	return idx
}

// ParseVoucherBalances reads a CSV export of opening voucher balances.
// decimals drives balance parsing via the EXISTING catimport.ParsePrice
// (the same decimal/comma normalization every other price cell in this
// package goes through — not reinvented here).
func ParseVoucherBalances(r io.Reader, decimals int) (VoucherBalanceResult, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // exports are ragged in the wild, same as Parse
	headers, err := cr.Read()
	if err != nil {
		return VoucherBalanceResult{}, fmt.Errorf("read header: %w", err)
	}
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\uFEFF") // Excel BOM
	}
	idx := voucherBalanceHeaderIndex(headers)
	if _, ok := idx["code"]; !ok {
		return VoucherBalanceResult{}, ErrNoCodeColumn
	}

	get := func(rec []string, field string) string {
		i, ok := idx[field]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	var res VoucherBalanceResult
	// firstRowByCode/dupRowsByCode: a row that passes its own checks is held
	// back here rather than appended straight to res.Items, because a LATER
	// row in the same file might still turn out to share its code — the
	// duplicate check can only be resolved once every row has been read.
	// firstRowByCode holds the first clean occurrence of each code (which
	// might end up as a real Item, or get demoted to an Issue below);
	// dupRowsByCode accumulates every occurrence AFTER the first, for a code
	// that turns out to repeat.
	firstRowByCode := map[string]VoucherBalanceItem{}
	var firstRowOrder []string // preserves file order for the eventual Items slice
	dupRowsByCode := map[string][]VoucherBalanceItem{}

	rowNum := 1 // header consumed above is row 1
	for {
		rec, rerr := cr.Read()
		if rerr == io.EOF {
			break
		}
		rowNum++
		if rerr != nil {
			return res, fmt.Errorf("row %d: %w", rowNum, rerr)
		}
		code := stripCSVDefuse(get(rec, "code"))
		if code == "" {
			res.Issues = append(res.Issues, VoucherBalanceRowIssue{RowNum: rowNum, Code: code, Reason: VoucherBalanceIssueMissingCode})
			continue
		}
		balanceRaw := get(rec, "balance")
		balanceMinor, perr := ParsePrice(balanceRaw, decimals)
		if perr != nil {
			res.Issues = append(res.Issues, VoucherBalanceRowIssue{RowNum: rowNum, Code: code, Reason: VoucherBalanceIssueBadBalance})
			continue
		}
		if balanceMinor <= 0 {
			res.Issues = append(res.Issues, VoucherBalanceRowIssue{RowNum: rowNum, Code: code, Reason: VoucherBalanceIssueZeroOrNegBalance})
			continue
		}
		item := VoucherBalanceItem{
			RowNum:       rowNum,
			Code:         code,
			BalanceMinor: balanceMinor,
			HolderLabel:  stripCSVDefuse(get(rec, "label")),
		}
		if _, taken := firstRowByCode[code]; !taken {
			firstRowByCode[code] = item
			firstRowOrder = append(firstRowOrder, code)
			continue
		}
		dupRowsByCode[code] = append(dupRowsByCode[code], item)
	}

	for _, code := range firstRowOrder {
		first := firstRowByCode[code]
		dups := dupRowsByCode[code]
		if len(dups) == 0 {
			res.Items = append(res.Items, first)
			continue
		}
		// A repeated code: neither the first occurrence nor any later one
		// imports — both/all are reported (see VoucherBalanceIssueDuplicateCodeInFile
		// doc comment above for why this format never dedupes/synthesizes,
		// unlike ParseBkp's PLU handling).
		res.Issues = append(res.Issues, VoucherBalanceRowIssue{RowNum: first.RowNum, Code: first.Code, Reason: VoucherBalanceIssueDuplicateCodeInFile})
		for _, d := range dups {
			res.Issues = append(res.Issues, VoucherBalanceRowIssue{RowNum: d.RowNum, Code: d.Code, Reason: VoucherBalanceIssueDuplicateCodeInFile})
		}
	}

	return res, nil
}
