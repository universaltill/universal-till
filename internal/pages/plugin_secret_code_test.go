package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6960 (ADR-0213 Decision 9): the plugin settings page's "Get the
// key from my." row posts the one-time code through the main till.

const testKeyCode = "ABCDE-FGHJK-MNPQR-STVWX-YZ012-3"

func seedKeyPlugin(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Db.Exec(`INSERT INTO plugin_catalog(id,version,name,description,runtime,entrypoint,package_url,sha256,author,website,tags_json,is_deprecated,min_pos_version,api_version,published_at) VALUES('sumup','1.0','SumUp','desc','go','entry','url','sha','auth','site','[]',0,'0.0.0','1',datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugins(id,name,version,entrypoint,is_active) VALUES('sumup','SumUp','1.0','entry',1)`); err != nil {
		t.Fatal(err)
	}
}

func postRedeem(mux *http.ServeMux, plugin, code string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+plugin+"/secret-code/redeem",
		strings.NewReader(url.Values{"code": {code}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func secretErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || b.Error == nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return b.Error.Code
}

func TestPluginSecretCodeRedeem(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedKeyPlugin(t, dp)
	dp.CloudSyncNow = make(chan struct{}, 1)
	var sent []string
	old := redeemKeyCode
	t.Cleanup(func() { redeemKeyCode = old })
	answer := cloudsync.RedeemAccepted
	redeemKeyCode = func(_ context.Context, _ *config.Config, code string) (cloudsync.RedeemOutcome, error) {
		sent = append(sent, code)
		return answer, nil
	}

	rec := postRedeem(mux, "sumup", testKeyCode)
	if rec.Code != http.StatusOK {
		t.Fatalf("accepted: %d %s", rec.Code, rec.Body.String())
	}
	if len(sent) != 1 || sent[0] != "ABCDEFGHJKMNPQRSTVWXYZ0123" {
		t.Fatalf("cloud got %v", sent)
	}
	select {
	case <-dp.CloudSyncNow:
	default:
		t.Fatal("an accepted code must kick an immediate check-in")
	}
	if strings.Contains(rec.Body.String(), "FGHJK") {
		t.Fatal("the code must not be echoed")
	}

	for outcome, want := range map[cloudsync.RedeemOutcome][2]any{
		cloudsync.RedeemRefused:     {http.StatusNotFound, "bad_code"},
		cloudsync.RedeemRateLimited: {http.StatusTooManyRequests, "rate_limited"},
		cloudsync.RedeemOffline:     {http.StatusServiceUnavailable, "offline"},
		cloudsync.RedeemUnavailable: {http.StatusServiceUnavailable, "offline"},
	} {
		answer = outcome
		rec := postRedeem(mux, "sumup", testKeyCode)
		if rec.Code != want[0].(int) || secretErrCode(t, rec) != want[1].(string) {
			t.Errorf("outcome %v: %d %s", outcome, rec.Code, rec.Body.String())
		}
	}
}

func TestPluginSecretCodeRedeem_Refusals(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedKeyPlugin(t, dp)
	calls := 0
	old := redeemKeyCode
	t.Cleanup(func() { redeemKeyCode = old })
	redeemKeyCode = func(context.Context, *config.Config, string) (cloudsync.RedeemOutcome, error) {
		calls++
		return cloudsync.RedeemAccepted, nil
	}
	// A plugin with no allow-listed secret has no code entry.
	if rec := postRedeem(mux, "p1", testKeyCode); rec.Code != http.StatusNotFound {
		t.Fatalf("other plugin: %d", rec.Code)
	}
	// A malformed code never reaches the cloud, same answer as a refusal.
	rec := postRedeem(mux, "sumup", "short")
	if rec.Code != http.StatusNotFound || secretErrCode(t, rec) != "bad_code" {
		t.Fatalf("malformed: %d %s", rec.Code, rec.Body.String())
	}
	// A satellite till is not the one that receives the key.
	if err := dp.Settings.Set(context.Background(), "sync.primary_url", "http://192.0.2.10:8080"); err != nil {
		t.Fatal(err)
	}
	rec = postRedeem(mux, "sumup", testKeyCode)
	if rec.Code != http.StatusConflict || secretErrCode(t, rec) != "satellite" {
		t.Fatalf("satellite: %d %s", rec.Code, rec.Body.String())
	}
	if calls != 0 {
		t.Fatalf("cloud called %d times", calls)
	}
}

func TestPluginSecretCodeStatus(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedKeyPlugin(t, dp)
	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/sumup/secret-code/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var b struct {
			Data struct {
				State string `json:"state"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return b.Data.State
	}
	if got := get(); got != "empty" {
		t.Fatalf("no key yet: %q", got)
	}
	seedPluginSetting(t, dp, "sumup", "sumup_api_key", "sumup-fake-test-key-0000000000000000", "global")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/sumup/secret-code/status", nil))
	if got := get(); got != "set" {
		t.Fatalf("key stored: %q", got)
	}
	if strings.Contains(rec.Body.String(), "fake-test-key") {
		t.Fatal("status leaks the key")
	}
}

func TestPluginSettingsPage_ShowsCodeRowOnlyForAllowListedPlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedKeyPlugin(t, dp)
	page := func(id string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/"+id+"/settings", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", id, rec.Code)
		}
		return rec.Body.String()
	}
	got := page("sumup")
	for _, k := range []string{"plugin.secret.get_from_my", "plugin.secret.scan", "plugin.secret.type", "plugin.secret.send_code", "plugin.secret.bad_code"} {
		if want := httpx.T("en", k); strings.HasPrefix(want, "plugin.secret.") || !strings.Contains(got, strings.ReplaceAll(want, "'", "&#39;")) {
			t.Errorf("page lacks %s (%q)", k, want)
		}
	}
	if !strings.Contains(got, `data-secret-code`) {
		t.Fatal("no code row")
	}
	if strings.Contains(page("p1"), `data-secret-code`) {
		t.Fatal("a plugin without an allow-listed secret must not get the row")
	}
	// Satellite: the buttons are replaced by the explanation.
	if err := dp.Settings.Set(context.Background(), "sync.primary_url", "http://192.0.2.10:8080"); err != nil {
		t.Fatal(err)
	}
	sat := page("sumup")
	if strings.Contains(sat, `data-secret-scan`) || !strings.Contains(sat, httpx.T("en", "plugin.secret.satellite")) {
		t.Fatal("satellite must show the main-till note and no buttons")
	}
}
