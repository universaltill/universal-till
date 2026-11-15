package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4792: a till registered before the terms step (or through a plugin
// install) accepts the cloud terms later, under Settings -> Till
// registration, via POST /api/enrol/terms.

type termsCloud2 struct {
	srv         *httptest.Server
	termsCalls  atomic.Int32
	mu          sync.Mutex
	body        map[string]any
	auth        string
	termsStatus int // 0 = 200
	termsBody   string
}

func (c *termsCloud2) sent() (map[string]any, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body, c.auth
}

// newRegisteredTill fakes a cloud, registers the till WITHOUT any terms
// (the pre-#4720 state) and returns the cloud.
func newRegisteredTill(t *testing.T, d *common.Deps) *termsCloud2 {
	t.Helper()
	c := &termsCloud2{}
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/stores/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"store_id":"store-t","merchant_id":"m-t","token":"tok-t"}}`))
	})
	m.HandleFunc("POST /api/v1/stores/terms", func(w http.ResponseWriter, r *http.Request) {
		c.termsCalls.Add(1)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		c.mu.Lock()
		c.body, c.auth = b, r.Header.Get("Authorization")
		status, body := c.termsStatus, c.termsBody
		c.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"store_id":"store-t","terms_version":"` + enroll.TermsVersion + `","terms_locale":"en","accepted_at":"2026-10-22T10:00:00Z"}}`))
	})
	c.srv = httptest.NewServer(m)
	t.Cleanup(c.srv.Close)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	d.Cfg.Marketplace.EndpointURL = c.srv.URL + "/api"
	d.Cfg.Marketplace.PublicKey = strings.Repeat("ab", 32)
	nameShop(t, d)
	if _, err := enroll.RegisterNow(t.Context(), d.Cfg, d.Settings); err != nil {
		t.Fatalf("seed registration: %v", err)
	}
	if !enroll.CurrentStatus().Registered {
		t.Fatal("seed registration did not register the till")
	}
	return c
}

func TestEnrolTerms_NoTickRecordsAndSendsNothing(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newRegisteredTill(t, d)

	rec := postForm(mux, "/api/enrol/terms", url.Values{}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="error"`) ||
		!strings.Contains(body, "Tick the box to accept the Universal Till cloud service terms.") {
		t.Fatalf("untick = %d %s, want 200 with the translated error", rec.Code, body)
	}
	if cloud.termsCalls.Load() != 0 || settingVal(t, d, "marketplace.terms_version") != "" {
		t.Fatal("an untick request recorded or sent an acceptance")
	}
}

// Validation runs before the elevation check, like /api/enrol/now.
func TestEnrolTerms_TickCheckedBeforeElevation(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = newRegisteredTill(t, d)
	rec := postForm(mux, "/api/enrol/terms", url.Values{}, &cashUser)
	if strings.Contains(rec.Body.String(), "elevation-dialog") || !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("cashier without tick = %s, want the error, not a PIN prompt", rec.Body.String())
	}
}

func TestEnrolTerms_ElevationPromptCarriesTheTick(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newRegisteredTill(t, d)
	rec := postForm(mux, "/api/enrol/terms", url.Values{"accept_terms": {"on"}}, &cashUser)
	body := rec.Body.String()
	if !strings.Contains(body, "elevation-dialog") || !strings.Contains(body, `name="accept_terms"`) ||
		!strings.Contains(body, "/api/enrol/terms") || !strings.Contains(body, "#terms-msg") {
		t.Fatalf("elevation prompt = %s, want a dialog posting /api/enrol/terms with accept_terms", body)
	}
	if cloud.termsCalls.Load() != 0 || settingVal(t, d, "marketplace.terms_version") != "" {
		t.Fatal("an unapproved request recorded or sent the acceptance")
	}
}

func TestEnrolTerms_TickRecordsSendsMarksAndAudits(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newRegisteredTill(t, d)

	rec := postForm(mux, "/api/enrol/terms", url.Values{"accept_terms": {"on"}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "✅") ||
		!strings.Contains(body, "Terms accepted: version "+enroll.TermsVersion) || strings.Contains(body, `class="error"`) {
		t.Fatalf("ticked = %d %s, want the check mark and the accepted line", rec.Code, body)
	}
	got, auth := cloud.sent()
	if got["store_id"] != "store-t" || got["terms_version"] != enroll.TermsVersion || got["terms_locale"] != "en" || auth != "Bearer tok-t" {
		t.Fatalf("cloud got %v / %q", got, auth)
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != enroll.TermsVersion {
		t.Fatalf("terms_version = %q", v)
	}
	if v := settingVal(t, d, "marketplace.terms_sent_version"); v != enroll.TermsVersion {
		t.Fatalf("terms_sent_version = %q, want the marker set", v)
	}
	var payload string
	if err := d.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'terms_accepted'`).Scan(&payload); err != nil || !strings.Contains(payload, enroll.TermsVersion) {
		t.Fatalf("audit payload %q (err %v), want terms_version", payload, err)
	}
}

// Offline: the acceptance is kept on the till, the marker stays unset, the
// operator sees a translated message and never the raw transport error.
func TestEnrolTerms_CloudDownKeepsAcceptanceLocally(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newRegisteredTill(t, d)
	cloud.srv.Close()

	rec := postForm(mux, "/api/enrol/terms", url.Values{"accept_terms": {"on"}}, &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "reach the cloud. Your acceptance is saved on this till") {
		t.Fatalf("cloud down = %d %s, want the translated retry message", rec.Code, body)
	}
	for _, raw := range []string{"connection refused", "127.0.0.1", "dial", "/v1/stores/terms"} {
		if strings.Contains(body, raw) {
			t.Fatalf("raw error text %q rendered: %s", raw, body)
		}
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != enroll.TermsVersion {
		t.Fatalf("acceptance not kept locally: %q", v)
	}
	if v := settingVal(t, d, "marketplace.terms_sent_version"); v != "" {
		t.Fatalf("sent marker %q set although the cloud was not reached", v)
	}
	var n int
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'terms_accepted'`).Scan(&n)
	if n != 0 {
		t.Fatal("audit row written for an acceptance the cloud never received")
	}
}

func TestEnrolTerms_ServiceRefusedShowsTheCloudUnavailableMessage(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newRegisteredTill(t, d)
	cloud.mu.Lock()
	cloud.termsStatus, cloud.termsBody = http.StatusForbidden, `{"error":{"code":"service_unavailable","message":"SECRET-REASON"}}`
	cloud.mu.Unlock()

	body := postForm(mux, "/api/enrol/terms", url.Values{"accept_terms": {"on"}}, &mgrUser).Body.String()
	if !strings.Contains(body, "The Universal Till cloud is not available for this shop.") || strings.Contains(body, "SECRET-REASON") {
		t.Fatalf("refused = %s", body)
	}
}

// Accepted earlier but never delivered: no tick is needed, the button resends.
func TestEnrolTerms_AcceptedButUnsentResendsWithoutTick(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	cloud := newRegisteredTill(t, d)
	if err := enroll.RecordTermsAcceptance(t.Context(), d.Settings, "en"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/enrol/terms", url.Values{}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✅") || cloud.termsCalls.Load() != 1 {
		t.Fatalf("resend = %s (calls %d)", rec.Body.String(), cloud.termsCalls.Load())
	}
	if v := settingVal(t, d, "marketplace.terms_sent_version"); v != enroll.TermsVersion {
		t.Fatalf("marker = %q after resend", v)
	}
}

func TestEnrolTerms_UnregisteredTillIsRefused(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	enroll.Init(t.Context(), &config.Config{}, d.Settings, &sync.WaitGroup{})
	if enroll.CurrentStatus().Registered {
		t.Skip("fixture is registered")
	}
	rec := postForm(mux, "/api/enrol/terms", url.Values{"accept_terms": {"on"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), `class="error"`) {
		t.Fatalf("unregistered = %s, want an error", rec.Body.String())
	}
	if v := settingVal(t, d, "marketplace.terms_version"); v != "" {
		t.Fatalf("acceptance %q recorded on an unregistered till", v)
	}
}

func TestSettingsPage_RegisteredTillTermsStates(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = newRegisteredTill(t, d)
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	// Not accepted: checkbox + Accept terms.
	body := get()
	for _, want := range []string{`id="registered-accept-terms"`, `hx-post="/api/enrol/terms"`, `hx-target="#terms-msg"`, "Accept terms"} {
		if !strings.Contains(body, want) {
			t.Fatalf("not-accepted registered page lacks %q", want)
		}
	}
	// Accepted, not sent: the line, the note and Send to the cloud; no box.
	if err := enroll.RecordTermsAcceptance(t.Context(), d.Settings, "en"); err != nil {
		t.Fatal(err)
	}
	body = get()
	if strings.Contains(body, `id="registered-accept-terms"`) || !strings.Contains(body, "has not reached the cloud yet") || !strings.Contains(body, "Send to the cloud") {
		t.Fatal("accepted-but-unsent registered page wrong")
	}
	// Accepted and sent: only the muted line.
	if err := d.Settings.Set(t.Context(), "marketplace.terms_sent_version", enroll.TermsVersion); err != nil {
		t.Fatal(err)
	}
	body = get()
	if strings.Contains(body, "Send to the cloud") || strings.Contains(body, `hx-post="/api/enrol/terms"`) || !strings.Contains(body, "Terms accepted: version "+enroll.TermsVersion) {
		t.Fatal("accepted+sent registered page still offers an action")
	}
}
