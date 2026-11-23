package pages

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6962: a plugin decline_code picks a specific, translated message;
// the code and any plugin text never reach the cashier.

func TestTenderDeclineKey_Table(t *testing.T) {
	exit := func(code string) *plugins.HandlerExitError {
		return &plugins.HandlerExitError{Code: 2, Stdout: json.RawMessage(`{"decline_code":"` + code + `"}`)}
	}
	cases := []struct {
		name string
		in   *plugins.HandlerExitError
		want string
	}{
		{"nil", nil, "pos.toast.payment_declined"},
		{"no stdout", &plugins.HandlerExitError{Code: 2}, "pos.toast.payment_declined"},
		{"card_declined", exit("card_declined"), "pos.toast.payment_declined"},
		{"unknown", exit("weird"), "pos.toast.payment_declined"},
		{"no_reader", exit("no_reader"), "pos.toast.payment_declined_no_reader"},
		{"reader_offline", exit("reader_offline"), "pos.toast.payment_declined_reader_offline"},
		{"reader_busy", exit("reader_busy"), "pos.toast.payment_declined_reader_busy"},
		{"auth_failed", exit("auth_failed"), "pos.toast.payment_declined_auth_failed"},
	}
	for _, c := range cases {
		if got := tenderDeclineKey(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestTender_DeclineCodeMapsTo402Message(t *testing.T) {
	cases := map[string]string{
		"card_declined":  "pos.toast.payment_declined",
		"weird":          "pos.toast.payment_declined",
		"no_reader":      "pos.toast.payment_declined_no_reader",
		"reader_offline": "pos.toast.payment_declined_reader_offline",
		"reader_busy":    "pos.toast.payment_declined_reader_busy",
		"auth_failed":    "pos.toast.payment_declined_auth_failed",
	}
	for code, key := range cases {
		t.Run(code, func(t *testing.T) {
			mux, dp := readerRig(t)
			rp := seedReaderPlugin(t, dp, "p.demo", "demopay", false)
			rp.answer = func(int) string { return "DECLINE:" + code }
			scan(t, dp)
			before := dp.Engine.Basket().Total
			rec := postTender(t, mux, tenderBody(leg("demopay", 120, "")))
			if rec.Code != http.StatusPaymentRequired {
				t.Fatalf("status %d, want 402: %s", rec.Code, rec.Body.String())
			}
			want := httpx.T("en", key)
			if want == key || want == "" {
				t.Fatalf("key %s missing from en.json", key)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != want {
				t.Fatalf("body %q, want %q", got, want)
			}
			if strings.Contains(rec.Body.String(), code) && code != "weird" && !strings.Contains(want, code) {
				t.Fatalf("decline code leaked: %s", rec.Body.String())
			}
			if countRows(t, dp, "SELECT COUNT(*) FROM sales") != 0 {
				t.Fatalf("a declined tender must not create a sale")
			}
			if dp.Engine.TenderLock() != "" {
				t.Fatalf("a definitive decline must not lock the tender")
			}
			if dp.Engine.Basket().Total != before || len(dp.Engine.Basket().Lines) == 0 {
				t.Fatalf("basket changed by a decline")
			}
		})
	}
}
