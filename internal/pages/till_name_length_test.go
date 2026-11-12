package pages

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5875: a till name over maxTillNameRunes is refused with 400 on the
// two LAN entry points, before the one-time token is spent.

func tcWantTooLong(t *testing.T, body string) {
	t.Helper()
	want := httpx.T("en", keyTillNameTooLong)
	if want == keyTillNameTooLong {
		t.Fatal("i18n: sync.error.name_too_long did not resolve")
	}
	if !strings.Contains(html.UnescapeString(body), want) {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestSyncEnroll_OverLongNameRefusedBeforeTokenSpent(t *testing.T) {
	long := strings.Repeat("é", maxTillNameRunes+1)
	for label, tokenOf := range map[string]func(t *testing.T, mux http.Handler) string{
		"long token": func(t *testing.T, mux http.Handler) string {
			_, tok, err := decodeEnrollCode(issueEnrolCode(t, mux, "http://192.168.1.10:8080"))
			if err != nil || tok == "" {
				t.Fatalf("decode code: %v", err)
			}
			return tok
		},
		"short code": func(t *testing.T, mux http.Handler) string {
			short, _, _ := issueShortCode(t, mux, "http://192.168.1.10:8080")
			return short
		},
	} {
		t.Run(label, func(t *testing.T) {
			t.Setenv("UT_AUTH", "off")
			initPagesI18n(t)
			mux, dp := newSyncAPITestDeps(t)
			token := tokenOf(t, mux)
			for _, name := range []string{long, "  " + long + "  "} {
				rec := tcEnrol(t, mux, token, name)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (never 422): %s", rec.Code, rec.Body.String())
				}
				tcWantTooLong(t, rec.Body.String())
			}
			var n int
			if err := dp.Db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM tills`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("tills rows = %d, %v; want none", n, err)
			}
			rec := tcEnrol(t, mux, token, "Bar")
			if rec.Code != http.StatusOK {
				t.Fatalf("retry with a valid name = %d: %s", rec.Code, rec.Body.String())
			}
			var out struct {
				Data struct {
					RegisterID string `json:"register_id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.RegisterID == "" {
				t.Fatalf("enrol response = %s (%v), want a register_id", rec.Body.String(), err)
			}
		})
	}
}

func TestSyncEnroll_SixtyRuneNameAccepted(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	mux, dp := newSyncAPITestDeps(t)
	_, token, err := decodeEnrollCode(issueEnrolCode(t, mux, "http://192.168.1.10:8080"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("é", maxTillNameRunes)
	rec := tcEnrol(t, mux, token, "  "+want+" ")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got string
	if err := dp.Db.QueryRowContext(t.Context(), `SELECT name FROM tills`).Scan(&got); err != nil || got != want {
		t.Fatalf("tills.name = %q, %v; want the 60-rune name", got, err)
	}
}

func TestPairRequest_OverLongDeviceNameRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	mux, dp, _ := newPairingAPITestDeps(t)
	rec := postPairRequest(t, mux, strings.Repeat("é", maxTillNameRunes+1), commitOf("secret-1"), "10.0.0.1:1234")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	tcWantTooLong(t, rec.Body.String())
	pending, err := data.NewPairingRepo(dp.Db).ListPending(t.Context())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %+v, %v; want none", pending, err)
	}
}

func TestPairRequest_SixtyRuneDeviceNameAccepted(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initPagesI18n(t)
	mux, dp, _ := newPairingAPITestDeps(t)
	want := strings.Repeat("é", maxTillNameRunes)
	rec := postPairRequest(t, mux, want, commitOf("secret-1"), "10.0.0.1:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	pending, err := data.NewPairingRepo(dp.Db).ListPending(t.Context())
	if err != nil || len(pending) != 1 || pending[0].DeviceName != want {
		t.Fatalf("pending = %+v, %v; want one with the 60-rune name", pending, err)
	}
}

func TestTillNameRefusalKey(t *testing.T) {
	for label, c := range map[string]struct{ in, want string }{
		"blank":         {"   ", ""},
		"sixty":         {strings.Repeat("é", maxTillNameRunes), ""},
		"sixty padded":  {" " + strings.Repeat("é", maxTillNameRunes) + " ", ""},
		"sixty one":     {strings.Repeat("é", maxTillNameRunes+1), keyTillNameTooLong},
		"rlo":           {"Bar‮abc", keyNameInvalidCharacters},
		"long plus rlo": {strings.Repeat("é", maxTillNameRunes+1) + "‮", keyNameInvalidCharacters},
	} {
		if got := tillNameRefusalKey(c.in); got != c.want {
			t.Errorf("%s: key = %q, want %q", label, got, c.want)
		}
	}
	if _, err := validateTillName(strings.Repeat("é", maxTillNameRunes+1)); !errors.Is(err, errTillNameTooLong) {
		t.Fatalf("err = %v, want errTillNameTooLong", err)
	}
}
