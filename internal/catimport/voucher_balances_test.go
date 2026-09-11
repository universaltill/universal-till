package catimport_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catimport"
)

func TestParseVoucherBalances_ValidRows(t *testing.T) {
	csv := "code,balance,label\nV001,25.00,Alice\nV002,10.50,Bob\n"
	res, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Issues) != 0 {
		t.Fatalf("expected no issues, got %v", res.Issues)
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(res.Items))
	}
	if res.Items[0].Code != "V001" || res.Items[0].BalanceMinor != 2500 || res.Items[0].HolderLabel != "Alice" {
		t.Fatalf("unexpected item 0: %+v", res.Items[0])
	}
	if res.Items[1].Code != "V002" || res.Items[1].BalanceMinor != 1050 || res.Items[1].HolderLabel != "Bob" {
		t.Fatalf("unexpected item 1: %+v", res.Items[1])
	}
}

func TestParseVoucherBalances_HolderLabelOptional(t *testing.T) {
	csv := "code,balance\nV001,25.00\n"
	res, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].HolderLabel != "" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestParseVoucherBalances_MissingCode(t *testing.T) {
	csv := "code,balance,label\n,25.00,Alice\nV002,10.50,Bob\n"
	res, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 clean item, got %d: %+v", len(res.Items), res.Items)
	}
	if len(res.Issues) != 1 {
		t.Fatalf("expected 1 issue, got %d: %+v", len(res.Issues), res.Issues)
	}
	if res.Issues[0].Reason != catimport.VoucherBalanceIssueMissingCode {
		t.Fatalf("expected missing_code reason, got %q", res.Issues[0].Reason)
	}
}

func TestParseVoucherBalances_BadBalance(t *testing.T) {
	csv := "code,balance,label\nV001,notanumber,Alice\nV002,10.50,Bob\n"
	res, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 clean item, got %d: %+v", len(res.Items), res.Items)
	}
	if len(res.Issues) != 1 || res.Issues[0].Reason != catimport.VoucherBalanceIssueBadBalance {
		t.Fatalf("expected bad_balance issue, got %+v", res.Issues)
	}
	if res.Issues[0].Code != "V001" {
		t.Fatalf("expected issue to report the row's code, got %+v", res.Issues[0])
	}
}

func TestParseVoucherBalances_ZeroOrNegativeBalanceRejected(t *testing.T) {
	// ParsePrice itself already rejects a negative amount as unparseable
	// (f < 0 -> error, see catimport.go), so a negative balance surfaces as
	// VoucherBalanceIssueBadBalance, not the ZeroOrNegBalance reason — the
	// ZeroOrNegBalance check below only ever catches what ParsePrice happily
	// parses AS a number but that is still not a positive liability: zero.
	// Both are rejected either way, which is the acceptance behaviour this
	// test proves; the exact reason code differs, deliberately, per the
	// "most specific single reason" rule in voucher_balances.go's doc comment.
	csv := "code,balance,label\nV001,0,Alice\nV002,-5.00,Bob\nV003,10.00,Carol\n"
	res, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Code != "V003" {
		t.Fatalf("expected only V003 to import cleanly, got %+v", res.Items)
	}
	if len(res.Issues) != 2 {
		t.Fatalf("expected 2 issues, got %+v", res.Issues)
	}
	byCode := map[string]catimport.VoucherBalanceRowIssue{}
	for _, iss := range res.Issues {
		byCode[iss.Code] = iss
	}
	if byCode["V001"].Reason != catimport.VoucherBalanceIssueZeroOrNegBalance {
		t.Fatalf("expected V001 (zero) to report non_positive_balance, got %+v", byCode["V001"])
	}
	if byCode["V002"].Reason != catimport.VoucherBalanceIssueBadBalance {
		t.Fatalf("expected V002 (negative) to report bad_balance (ParsePrice rejects negatives), got %+v", byCode["V002"])
	}
}

func TestParseVoucherBalances_DuplicateCodeInFile_BothReportedNeitherKept(t *testing.T) {
	csv := "code,balance,label\nV001,10.00,Alice\nV002,20.00,Bob\nV001,15.00,Alice2\n"
	res, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Code != "V002" {
		t.Fatalf("expected only V002 to import cleanly, got %+v", res.Items)
	}
	var dupIssues []catimport.VoucherBalanceRowIssue
	for _, iss := range res.Issues {
		if iss.Code == "V001" {
			dupIssues = append(dupIssues, iss)
		}
	}
	if len(dupIssues) != 2 {
		t.Fatalf("expected BOTH V001 rows reported as duplicates, got %+v", dupIssues)
	}
	for _, iss := range dupIssues {
		if iss.Reason != catimport.VoucherBalanceIssueDuplicateCodeInFile {
			t.Fatalf("expected duplicate_code_in_file reason, got %+v", iss)
		}
	}
	// RowNum for the two duplicate occurrences must be distinct and identify
	// the actual file rows (2 and 4: header is row 1, V002 is row 3).
	if dupIssues[0].RowNum == dupIssues[1].RowNum {
		t.Fatalf("expected distinct row numbers for the two duplicate occurrences, got %+v", dupIssues)
	}
}

func TestParseVoucherBalances_DecimalCommaNormalizesViaParsePrice(t *testing.T) {
	// Just proves the wiring calls catimport.ParsePrice correctly for
	// decimal-comma input — ParsePrice's own normalization is tested
	// elsewhere in this package.
	csv := "code,balance,label\nV001,\"25,50\",Alice\n"
	res, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].BalanceMinor != 2550 {
		t.Fatalf("expected decimal-comma balance to parse as 2550, got %+v", res.Items)
	}
}

func TestParseVoucherBalances_MissingHeader_ReturnsSentinelError(t *testing.T) {
	csv := "name,price\nWidget,9.99\n"
	_, err := catimport.ParseVoucherBalances(strings.NewReader(csv), 2)
	if !errors.Is(err, catimport.ErrNoCodeColumn) {
		t.Fatalf("expected ErrNoCodeColumn, got %v", err)
	}
}
