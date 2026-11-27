package pages

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settlementfee"
)

// ut-docs#7160 (epic #7061): the day-close screen shows the Stripe fee line
// the cloud imported. Display only: no fee maths, no flag in the till.

// The fixtures change the working directory, so paths resolve from this file.
func here(rel string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), rel)
}

func dayXGolden() string { return here("testdata/eod_day_x_no_fee_7160.golden.html") }

var digitRun = regexp.MustCompile(`[0-9]`)

// maskDigits hides the clock stamps (the report is "as of now"); the
// structure, labels and layout are what the golden pins.
func maskDigits(s string) string { return digitRun.ReplaceAllString(s, "0") }

func withFeeFetcher(t *testing.T, f func(context.Context, string) ([]settlementfee.Line, error)) {
	t.Helper()
	old := settlementFeeFetcher
	settlementFeeFetcher = func(*common.Deps) settlementfee.Fetcher { return f }
	t.Cleanup(func() { settlementFeeFetcher = old })
}

func stripeLine(minor int64) settlementfee.Line {
	return settlementfee.Line{Provider: "stripe", LocaleKey: "reports.eod.stripe_fee", Currency: "GBP", Fee: money.FromMinor(minor)}
}

// The cloud sends no fee data (flag off, SumUp or own-key day): the day-X
// answer is byte-identical (clock digits masked) to what it was before this
// card.
func TestDayXFee_AbsentDataLeavesOutputUnchanged(t *testing.T) {
	f := dxFixture(t)
	withFeeFetcher(t, func(context.Context, string) ([]settlementfee.Line, error) { return nil, nil })
	body := maskDigits(f.post(t, dxView, url.Values{}, nil, nil).Body.String())
	want, err := os.ReadFile(dayXGolden())
	if err != nil {
		t.Fatal(err)
	}
	if body != string(want) {
		t.Errorf("day-X output changed with no fee data:\n%s", body)
	}
}

func TestDayXFee_FetchFailureLeavesOutputUnchanged(t *testing.T) {
	f := dxFixture(t)
	withFeeFetcher(t, func(context.Context, string) ([]settlementfee.Line, error) { return nil, errors.New("offline") })
	rec := f.post(t, dxView, url.Values{}, nil, nil)
	want, _ := os.ReadFile(dayXGolden())
	if rec.Code != 200 || maskDigits(rec.Body.String()) != string(want) {
		t.Errorf("a failed fetch must not change or block the report: %d", rec.Code)
	}
}

func TestDayXFee_NoFetcherConfiguredLeavesOutputUnchanged(t *testing.T) {
	f := dxFixture(t) // test deps have no cloud credentials
	rec := f.post(t, dxView, url.Values{}, nil, nil)
	want, _ := os.ReadFile(dayXGolden())
	if rec.Code != 200 || maskDigits(rec.Body.String()) != string(want) {
		t.Errorf("no credentials: report must be unchanged: %d", rec.Code)
	}
}

// 123 is deliberately not 0.2% of anything on the fixture day: the screen
// shows the imported figure, it does not compute one.
func TestDayXFee_ShowsImportedAmountVerbatim(t *testing.T) {
	f := dxFixture(t)
	var gotDate string
	withFeeFetcher(t, func(_ context.Context, date string) ([]settlementfee.Line, error) {
		gotDate = date
		return []settlementfee.Line{stripeLine(123)}, nil
	})
	body := f.post(t, dxView, url.Values{}, nil, nil).Body.String()
	if !strings.Contains(body, "Stripe fees (card payments)") {
		t.Fatalf("label missing: %s", body)
	}
	if !strings.Contains(body, "£1.23") {
		t.Errorf("amount must be the imported 123 minor units: %s", body)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(gotDate) {
		t.Errorf("fetched for date %q", gotDate)
	}
}

func TestDayXFee_OneRowPerLine(t *testing.T) {
	f := dxFixture(t)
	withFeeFetcher(t, func(context.Context, string) ([]settlementfee.Line, error) {
		return []settlementfee.Line{stripeLine(123), stripeLine(7)}, nil
	})
	body := f.post(t, dxView, url.Values{}, nil, nil).Body.String()
	if n := strings.Count(body, `class="eod-day-x-fee-row"`); n != 2 {
		t.Errorf("rows = %d, want 2", n)
	}
}

// The fee never reaches the printed day-X paper, the JSON report or the
// archived Z: screen only.
func TestDayXFee_ScreenOnly(t *testing.T) {
	f := dxFixture(t)
	withFeeFetcher(t, func(context.Context, string) ([]settlementfee.Line, error) {
		return []settlementfee.Line{stripeLine(123)}, nil
	})
	rep, err := readEODDayX(t.Context(), f.dp, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	doc := buildEODDayXDoc(rep, "Shop", "", "capped", 20)
	paper := strings.Join(append(append([]string{}, doc.Meta...), doc.Footer...), "\n")
	if strings.Contains(paper, "1.23") || strings.Contains(strings.ToLower(paper), "stripe") {
		t.Errorf("fee on printed paper: %s", paper)
	}
	rec := f.post(t, dxView, url.Values{}, jsonHdr, nil)
	if strings.Contains(rec.Body.String(), "stripe") || strings.Contains(rec.Body.String(), "fee") {
		t.Errorf("fee in JSON report: %s", rec.Body.String())
	}
}

// A cashier is sent to the manager-PIN prompt, which carries no fee line and
// triggers no fetch (#3079).
func TestDayXFee_CashierSeesNoFeeAndNoFetch(t *testing.T) {
	f := dxFixture(t)
	t.Setenv("UT_AUTH", "on")
	_, cashierID := newElevationTestPrincipals(t, f.dp, "mgr-f", "cash-f", "445566")
	calls := 0
	withFeeFetcher(t, func(context.Context, string) ([]settlementfee.Line, error) {
		calls++
		return []settlementfee.Line{stripeLine(123)}, nil
	})
	rec := f.post(t, dxView, url.Values{}, nil, &auth.User{ID: cashierID, Role: "cashier"})
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" || calls != 0 || strings.Contains(rec.Body.String(), "Stripe fees") {
		t.Errorf("cashier: prompt=%q calls=%d", rec.Header().Get("X-UT-Response"), calls)
	}
}

// The till holds no rate: no basis points, no 0.2 constant in the non-test
// sources of the fee feature.
func TestNoRateConstantInTill(t *testing.T) {
	bad := regexp.MustCompile(`(?i)\bbps\b|basis.?point|0\.002|\b0\.2\b|platform.?fee|application.?fee`)
	for _, p := range []string{"eod_stripe_fee_7160.go", "../settlementfee/settlementfee.go"} {
		b, err := os.ReadFile(here(p))
		if err != nil {
			t.Fatal(err)
		}
		for i, ln := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "//") {
				continue
			}
			if bad.MatchString(ln) {
				t.Errorf("%s:%d names a rate/fee constant: %s", p, i+1, ln)
			}
		}
	}
}
