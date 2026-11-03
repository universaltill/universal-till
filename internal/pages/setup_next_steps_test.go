package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5058 UT-3 (design-ut3 N1-N5): the "Next steps" card on the sale
// screen after a till was set up from the shop's cloud account.

const nextStepsKey = "cloudsync.next_steps"

func nextStepsValue(t *testing.T, d *common.Deps) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), nextStepsKey)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func nextStepsIndex(t *testing.T, d *common.Deps, user string) string {
	t.Helper()
	mux := http.NewServeMux()
	registerIndex(mux, d)
	registerNextStepsUI(mux, d)
	u := mgrUser
	if user == "cashier" {
		u = cashUser
	}
	rec := getAs(mux, "/", &u)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: %d %.300s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// N1: a cloud setup writes pending:<mode>; a local setup and a failed commit
// write nothing.
func TestCloudSetupMarksNextSteps(t *testing.T) {
	for name, tc := range map[string]struct {
		master string
		want   string
	}{
		"till":     {"till", "pending:till"},
		"cloud":    {"cloud", "pending:cloud"},
		"adopting": {"adopting", "pending:adopting"},
		"unknown":  {"weird", "pending:"},
		"absent":   {"", "pending:"},
	} {
		t.Run(name, func(t *testing.T) {
			mux, d, stub, nonce := cloudWizard(t)
			reply := strings.Replace(cloudStoreAnswer, `"catalog_master":"cloud"`, `"catalog_master":"`+tc.master+`"`, 1)
			stub.set(200, reply)
			doCloudPair(mux, nonce, nil)
			if rec := finalCloudPost(mux, nonce, nil); rec.Code != http.StatusSeeOther {
				t.Fatalf("final post: %d %.300s", rec.Code, rec.Body.String())
			}
			if got := nextStepsValue(t, d); got != tc.want {
				t.Fatalf("%s = %q, want %q", nextStepsKey, got, tc.want)
			}
		})
	}
}

func TestCloudSetupWithFailedCommitWritesNoNextSteps(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	prev := commitCloudPairing
	commitCloudPairing = func(context.Context, *common.Deps, *enroll.FirstTillPairing) error { return errors.New("boom") }
	t.Cleanup(func() { commitCloudPairing = prev })
	rec := finalCloudPost(mux, nonce, nil)
	if !strings.Contains(rec.Header().Get("Location"), "pairfail=1") {
		t.Fatalf("expected pairfail redirect, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := nextStepsValue(t, d); got != "" {
		t.Fatalf("a failed pairing left %s=%q", nextStepsKey, got)
	}
}

// N2 + N3: who sees the card, and the no_catalog line's four cases.
func TestNextStepsCardVisibility(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	has := func(body string) bool { return strings.Contains(body, `data-testid="next-steps-card"`) }

	if has(nextStepsIndex(t, d, "manager")) {
		t.Fatal("card shown with no key")
	}
	_ = d.Settings.Set(ctx, nextStepsKey, "done") // settings-write:allow test
	if has(nextStepsIndex(t, d, "manager")) {
		t.Fatal("card shown when done")
	}
	_ = d.Settings.Set(ctx, nextStepsKey, "pending:cloud") // settings-write:allow test
	body := nextStepsIndex(t, d, "manager")
	for _, want := range []string{`data-testid="next-steps-card"`, "Next steps", `href="/users"`, `href="/settings#settings-payments"`, `href="/inventory"`, `data-testid="next-steps-dismiss"`, `hx-post="/ui/next-steps/dismiss"`} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q", want)
		}
	}
	if strings.Contains(body, "does not keep products in the cloud") {
		t.Error("cloud mode shows the no_catalog line")
	}
	if i := strings.Index(body, `id="next-steps-card"`); i < 0 || strings.Contains(body[i:i+400], "<dialog") || strings.Contains(body[i-40:i+400], "pos-notice") {
		t.Error("card is missing its section, or is a dialog/pos-notice")
	}
	if has(nextStepsIndex(t, d, "cashier")) {
		t.Fatal("a cashier sees the card")
	}
}

func TestNextStepsNoCatalogLine(t *testing.T) {
	const line = "does not keep products in the cloud"
	cases := []struct {
		name, stored, cache string
		want                bool
	}{
		{"stored till, no cache", "pending:till", "", true},
		{"stored cloud, no cache", "pending:cloud", "", false},
		{"stored adopting, no cache", "pending:adopting", "", false},
		{"nothing known", "pending:", "", false},
		{"cache says till beats stored cloud", "pending:cloud", "till", true},
		{"cache says cloud beats stored till", "pending:till", "cloud", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, d := newFullAuthDeps(t)
			_ = d.Settings.Set(t.Context(), nextStepsKey, tc.stored) // settings-write:allow test
			if tc.cache != "" {
				_ = d.Settings.Set(t.Context(), entitlement.KeyCatalogMaster, tc.cache) // settings-write:allow test
			}
			if got := strings.Contains(nextStepsIndex(t, d, "manager"), line); got != tc.want {
				t.Fatalf("no_catalog line shown = %v, want %v", got, tc.want)
			}
		})
	}
}

// N4: dismiss.
func TestNextStepsDismiss(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	registerNextStepsUI(mux, d)
	_ = d.Settings.Set(t.Context(), nextStepsKey, "pending:till") // settings-write:allow test

	if rec := getAs(mux, "/ui/next-steps/dismiss", &mgrUser); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET dismiss = %d, want 405", rec.Code)
	}
	rec := postForm(mux, "/ui/next-steps/dismiss", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Manager") {
		t.Fatalf("cashier dismiss = %d %s", rec.Code, rec.Body.String())
	}
	if got := nextStepsValue(t, d); got != "pending:till" {
		t.Fatalf("cashier changed the value to %q", got)
	}
	rec = postForm(mux, "/ui/next-steps/dismiss", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("manager dismiss = %d %q", rec.Code, rec.Body.String())
	}
	if got := nextStepsValue(t, d); got != "done" {
		t.Fatalf("value = %q, want done", got)
	}
	if strings.Contains(nextStepsIndex(t, d, "manager"), `data-testid="next-steps-card"`) {
		t.Fatal("card still shown after dismiss")
	}
}

// N5: per till, so a joined replica never inherits (or shows) it.
func TestNextStepsSettingIsPerTill(t *testing.T) {
	if got := data.SettingScope(nextStepsSettingKey); got != data.SettingPerTill {
		t.Fatalf("SettingScope(%q) = %v, want per-till", nextStepsSettingKey, got)
	}
}

// N1 (tester, ut-docs#5058): a local setup writes no next-steps marker and
// no receipt prefix.
func TestLocalSetupWritesNoNextSteps(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := postForm(mux, "/api/setup", url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"}, "country": {"GB"}, "currency": {"GBP"},
		"tax_rate_pct": {"20"}, "store_name": {"Corner Shop"}, "till_name": {"Till 1"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("local setup: %d %.300s", rec.Code, rec.Body.String())
	}
	if got := nextStepsValue(t, d); got != "" {
		t.Fatalf("local setup wrote %s=%q", nextStepsKey, got)
	}
	if got, _, _ := d.Settings.Get(t.Context(), "sync.receipt_prefix"); got != "" {
		t.Fatalf("local setup wrote sync.receipt_prefix=%q", got)
	}
}
