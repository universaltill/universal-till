package settlementfee

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// The fixture's fee (123) is deliberately NOT 0.2% of the 50000 card total
// (which would be 100): the till shows the imported figure, it never
// computes one (ut-docs#7160 acceptance 1).
// has stands in for the loaded English pack.
func has(k string) bool { return k == "reports.eod.stripe_fee" }

const fixtureBody = `{"date":"2030-03-05","currency":"GBP","card_total_minor":50000,` +
	`"provider_fee_lines":[{"provider":"stripe","label_key":"day_close.stripe_fee","fee_minor":123}]}`

func TestParse_FixtureFeeShownVerbatim(t *testing.T) {
	got, err := Parse([]byte(fixtureBody), has)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("lines = %d, want 1", len(got))
	}
	if got[0].Fee != money.FromMinor(123) {
		t.Errorf("fee = %d, want exactly 123", got[0].Fee.Minor())
	}
	if got[0].Currency != "GBP" || got[0].Provider != "stripe" || got[0].LocaleKey != "reports.eod.stripe_fee" {
		t.Errorf("line = %+v", got[0])
	}
}

func TestParse_AcceptsDataEnvelope(t *testing.T) {
	got, err := Parse([]byte(`{"data":`+fixtureBody+`,"error":null}`), has)
	if err != nil || len(got) != 1 || got[0].Fee.Minor() != 123 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestParse_NoRowCases(t *testing.T) {
	cases := map[string]string{
		"field absent":        `{"date":"2030-03-05","currency":"GBP","card_total_minor":50000}`,
		"field null":          `{"currency":"GBP","provider_fee_lines":null}`,
		"empty array":         `{"currency":"GBP","provider_fee_lines":[]}`,
		"zero fee":            `{"currency":"GBP","provider_fee_lines":[{"provider":"stripe","label_key":"day_close.stripe_fee","fee_minor":0}]}`,
		"negative fee":        `{"currency":"GBP","provider_fee_lines":[{"provider":"stripe","label_key":"day_close.stripe_fee","fee_minor":-5}]}`,
		"unknown label key":   `{"currency":"GBP","provider_fee_lines":[{"provider":"stripe","label_key":"evil.<b>x</b>","fee_minor":5}]}`,
		"no locale key":       `{"currency":"GBP","provider_fee_lines":[{"provider":"sumup","label_key":"day_close.sumup_fee","fee_minor":5}]}`,
		"bad provider":        `{"currency":"GBP","provider_fee_lines":[{"provider":"X Y","label_key":"day_close.stripe_fee","fee_minor":5}]}`,
		"bad currency":        `{"currency":"gbp!","provider_fee_lines":[{"provider":"stripe","label_key":"day_close.stripe_fee","fee_minor":5}]}`,
		"fee as float string": `{"currency":"GBP","provider_fee_lines":[{"provider":"stripe","label_key":"day_close.stripe_fee","fee_minor":"1.5"}]}`,
		"empty body":          ``,
		"empty object":        `{}`,
	}
	for name, body := range cases {
		got, err := Parse([]byte(body), has)
		if len(got) != 0 {
			t.Errorf("%s: got rows %+v, want none (err %v)", name, got, err)
		}
	}
}

func TestParse_CapsLines(t *testing.T) {
	line := `{"provider":"stripe","label_key":"day_close.stripe_fee","fee_minor":1}`
	body := `{"currency":"GBP","provider_fee_lines":[` + strings.Repeat(line+",", MaxLines+5) + line + `]}`
	got, _ := Parse([]byte(body), has)
	if len(got) != MaxLines {
		t.Errorf("lines = %d, want cap %d", len(got), MaxLines)
	}
}

func TestFetcher_GetsContractPathWithBearer(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(fixtureBody))
	}))
	defer srv.Close()
	f := NewHTTPFetcher(srv.Client(), srv.URL+"/", "till-1", "tok", has)
	lines, err := f(context.Background(), "2030-03-05")
	if err != nil || len(lines) != 1 || lines[0].Fee.Minor() != 123 {
		t.Fatalf("lines %+v err %v", lines, err)
	}
	if gotPath != "/v1/tills/till-1/settlement-summary?date=2030-03-05" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header wrong")
	}
}

func TestFetcher_FailuresYieldNoLines(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"404":  func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		"500":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"junk": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
	} {
		srv := httptest.NewServer(h)
		f := NewHTTPFetcher(srv.Client(), srv.URL, "t", "tok", has)
		lines, _ := f(context.Background(), "2030-03-05")
		srv.Close()
		if len(lines) != 0 {
			t.Errorf("%s: got %+v", name, lines)
		}
	}
}

func TestFetcher_BadDateNeverSent(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()
	f := NewHTTPFetcher(srv.Client(), srv.URL, "t", "tok", has)
	if _, err := f(context.Background(), "../x?y=1"); err == nil || called {
		t.Fatalf("bad date must be refused before any request (err %v called %v)", err, called)
	}
}

func TestFetcher_HonoursTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	f := NewHTTPFetcher(srv.Client(), srv.URL, "t", "tok", has)
	start := time.Now()
	lines, err := f(context.Background(), "2030-03-05")
	if len(lines) != 0 || err == nil {
		t.Fatalf("want error and no lines, got %+v %v", lines, err)
	}
	if time.Since(start) > Timeout+time.Second {
		t.Errorf("took %s, want about %s", time.Since(start), Timeout)
	}
}

func TestParse_NilHasDropsEverything(t *testing.T) {
	got, _ := Parse([]byte(fixtureBody), nil)
	if len(got) != 0 {
		t.Errorf("got %+v", got)
	}
}
