package pages

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#2925: the money fields #2819 didn't reach still used the dot-only
// {{ moneypattern }}, so a German/Turkish keyboard's "3,50" was blocked by
// native validation in the device OS language. Every money input's value
// is now read by a comma-tolerant parser -- httpx.ParseMoneyMajor on the
// server, window.utCurrency.toMinor/parseMinor in the browser -- so no
// template may keep the dot-only pattern.
func TestMoneyTemplates_NoDotOnlyMoneyPattern(t *testing.T) {
	chdirRoot(t)
	dotOnly := regexp.MustCompile(`\{\{-?\s*moneypattern\s`)
	var hits []string
	err := filepath.WalkDir(filepath.Join("web", "ui"), func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if dotOnly.MatchString(line) {
				hits = append(hits, path+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("money inputs still using the dot-only {{ moneypattern }} (use {{ moneypatternlocal }}; ut-docs#2925):\n%s", strings.Join(hits, "\n"))
	}
}

// ut-docs#2925: a German "3,50" promotion value reaches the server as typed;
// it used strconv.ParseFloat, which refused it (and accepted "1e3").
func TestPromotionsCreate_ValueAmountAcceptsDecimalComma(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"KOMMA350"}, "type": {"amount"}, "value_amount": {"3,50"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("create 3,50: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var value int64
	if err := d.Db.QueryRow(`SELECT value FROM promotions WHERE code = 'KOMMA350'`).Scan(&value); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if value != 350 {
		t.Fatalf("value = %d, want 350 minor units", value)
	}

	for _, bad := range []string{"1e3", "0x10", "3,505", "-3,50", "0,00"} {
		rec = postForm(mux, "/api/promotions", url.Values{
			"code": {"BAD" + strings.NewReplacer(",", "", ".", "", "-", "").Replace(bad)}, "type": {"amount"}, "value_amount": {bad},
		}, &manager)
		if rec.Header().Get("Location") != "/promotions?err=promotions.error.value_invalid" {
			t.Errorf("value_amount %q: loc=%q, want value_invalid", bad, rec.Header().Get("Location"))
		}
	}
}

// ut-docs#2925: the payments-fee fixed amount accepts a decimal comma and
// refuses a malformed amount instead of silently storing 0.
func TestPaymentsFee_FixedAcceptsDecimalComma(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/settings/payments-fee", url.Values{
		"method": {"card"}, "percent": {"1.5"}, "fixed": {"0,30"},
	}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("fixed 0,30: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var fee struct {
		BP    int64 `json:"bp"`
		Fixed int64 `json:"fixed"`
	}
	raw, _, _ := d.Settings.Get(t.Context(), "payments.fee.card")
	if err := json.Unmarshal([]byte(raw), &fee); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if fee.BP != 150 || fee.Fixed != 30 {
		t.Fatalf("stored fee = %+v, want {BP:150 Fixed:30}", fee)
	}

	// Empty stays a zero fixed fee, as before.
	rec = postForm(mux, "/api/settings/payments-fee", url.Values{"method": {"cash"}, "percent": {"1"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("empty fixed: %s", rec.Body.String())
	}

	for _, bad := range []string{"1e3", "abc", "0,305", "-1"} {
		rec = postForm(mux, "/api/settings/payments-fee", url.Values{"method": {"card"}, "percent": {"1"}, "fixed": {bad}}, &mgrUser)
		if !strings.Contains(rec.Body.String(), "range") {
			t.Errorf("fixed %q: body=%s, want the range refusal", bad, rec.Body.String())
		}
	}
	raw, _, _ = d.Settings.Get(t.Context(), "payments.fee.card")
	if err := json.Unmarshal([]byte(raw), &fee); err != nil || fee.Fixed != 30 {
		t.Fatalf("a refused fixed fee changed the stored one: %q", raw)
	}
}
