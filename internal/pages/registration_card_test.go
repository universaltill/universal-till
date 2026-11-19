package pages

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/enroll"
)

// ADR-0200 (ut-docs#6559): registration is automatic. No opt-in checkbox, no
// Register button; the wizard's last screen shows the notice, Finish stores
// that it was shown (and never calls the cloud itself), and Settings has a
// status-only Privacy card with a Send usage facts switch. The notice wording
// is the owner-approved checkin-notice-v2 text.

func wizardForm() url.Values {
	return url.Values{
		"pin": {"2468"}, "pin_confirm": {"2468"},
		"country": {"GB"}, "currency": {"GBP"},
		"store_name": {"Corner Shop"}, "till_name": {"Till 1"},
	}
}

func TestSetupWizardShowsTheNoticeNotAnOptInCheckbox(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: code=%d", rec.Code)
	}
	body := rec.Body.String()
	for _, bad := range []string{`name="auto_register"`, `autoReg`, `name="accept_terms"`} {
		if strings.Contains(body, bad) {
			t.Errorf("wizard still renders %q: registration is automatic (ADR-0200), nothing to tick", bad)
		}
	}
	for _, want := range []string{`data-testid="registration-notice"`, "What this till tells us", "We never send your sales, customers, staff, items or anything you type"} {
		if !strings.Contains(body, want) {
			t.Errorf("wizard's last screen lacks %q", want)
		}
	}
}

// Finish stores that the notice was shown BEFORE anything could be sent, and
// makes no cloud call on the request path: the background loop registers.
func TestSetupWizardFinishStoresNoticeShownAndCallsNoCloud(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()
	if enroll.NoticeShown(t.Context(), d.Settings) {
		t.Fatal("notice already shown on a fresh till")
	}
	form := wizardForm()
	form.Set("auto_register", "on") // a stale client: ignored
	rec := postForm(mux, "/api/setup", form, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !enroll.NoticeShown(t.Context(), d.Settings) {
		t.Fatal("finishing the wizard did not store that the notice was shown")
	}
	if v := settingVal(t, d, enroll.KeyNoticeVersion); v != enroll.NoticeVersion {
		t.Errorf("notice version = %q, want %q", v, enroll.NoticeVersion)
	}
	if hits := mkt.storeRegisterHits(); hits != 0 {
		t.Fatalf("the setup request called the cloud %d time(s); registration runs in the background loop (ADR-0003)", hits)
	}
	if sessionCookie(rec) == "" {
		t.Fatal("wizard did not sign the admin in")
	}
}

func TestSettingsCloudRecordCardIsStatusOnlyWithNotice(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := getAs(mux, "/settings", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="settings-privacy"`, `data-testid="cloud-record"`, "Cloud record", `data-testid="registration-notice"`, "Send usage facts", "Not registered yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings lacks %q", want)
		}
	}
	for _, bad := range []string{`/api/enrol/now`, `/api/settings/auto-register`, "Register this till with the marketplace automatically"} {
		if strings.Contains(body, bad) {
			t.Errorf("Settings still carries %q: there is no Register button or opt-in any more", bad)
		}
	}
	// name="optIn" is also the unrelated telemetry form, so only the Privacy
	// card (the #settings-privacy block) is checked for it.
	if at := strings.Index(body, `id="settings-privacy"`); at >= 0 {
		end := at + 3000
		if end > len(body) {
			end = len(body)
		}
		if strings.Contains(body[at:end], `name="optIn"`) {
			t.Error("the Privacy card carries an opt-in checkbox")
		}
	}
	if !enroll.NoticeShown(t.Context(), d.Settings) {
		t.Error("showing the Privacy card did not store that the notice was shown")
	}
}

func TestSettingsCloudRecordCardIsNotShownToACashier(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := getAs(mux, "/settings", &cashUser)
	if strings.Contains(rec.Body.String(), `data-testid="cloud-record"`) {
		t.Error("a cashier sees the Cloud record card")
	}
	if enroll.NoticeShown(t.Context(), d.Settings) {
		t.Error("the notice was marked shown by a session that could not see it")
	}
}

func TestRegistrationStopRequiresElevationForACashier(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/registration-stop", url.Values{"stop": {"1"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier stop: code=%d body=%s, want the elevation prompt", rec.Code, rec.Body.String())
	}
	if enroll.Stopped(t.Context(), d.Settings) {
		t.Fatal("a denied stop was persisted")
	}
}

func TestRegistrationStopAndResumeAreAuditedAndPersist(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/registration-stop", url.Values{"stop": {"1"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager stop: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !enroll.Stopped(t.Context(), d.Settings) {
		t.Fatal("stop did not persist")
	}
	if cs := enroll.CurrentCardStatus(t.Context(), d.Settings); cs.State != enroll.StatePaused {
		t.Fatalf("card state after stop = %q, want paused", cs.State)
	}
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, "Paused: this till sends nothing about itself.") {
		t.Error("paused card does not say so")
	}
	if tag := regexp.MustCompile(`<input[^>]*id="registration-send-cb"[^>]*>`).FindString(body); tag == "" || regexp.MustCompile(`\schecked(\s|>)`).MatchString(tag) {
		t.Errorf("paused: the Send usage facts switch must be present and OFF, got %q", tag)
	}
	if rec := postForm(mux, "/api/settings/registration-stop", url.Values{"stop": {"0"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("manager resume: code=%d", rec.Code)
	}
	if enroll.Stopped(t.Context(), d.Settings) {
		t.Fatal("resume did not persist")
	}
	var stopped, resumed int
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'registration_stopped'`).Scan(&stopped)
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'registration_resumed'`).Scan(&resumed)
	if stopped != 1 || resumed != 1 {
		t.Fatalf("audit rows stopped=%d resumed=%d, want 1 and 1", stopped, resumed)
	}
}

// A request whose `stop` is missing or not exactly "0"/"1" is refused: it must
// never be read as "resume" (the switch builds `stop` in an hx-vals js: value,
// which a stricter CSP could drop), and nothing changes, is audited or wakes
// the registration loop.
func TestRegistrationStopRefusesAMissingOrGarbledStop(t *testing.T) {
	for _, startStopped := range []bool{false, true} {
		for _, form := range []url.Values{{}, {"stop": {"x"}}, {"stop": {""}}, {"stop": {"true"}}, {"stop": {"01"}}} {
			mux, _, d := newFullAuthDeps(t)
			if startStopped {
				if rec := postForm(mux, "/api/settings/registration-stop", url.Values{"stop": {"1"}}, &mgrUser); rec.Code != http.StatusNoContent {
					t.Fatalf("seed stop: code=%d", rec.Code)
				}
			}
			var before int
			_ = d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'registration_%'`).Scan(&before)
			rec := postForm(mux, "/api/settings/registration-stop", form, &mgrUser)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("stopped=%v form=%v: code=%d, want 400", startStopped, form, rec.Code)
			}
			if got := enroll.Stopped(t.Context(), d.Settings); got != startStopped {
				t.Errorf("stopped=%v form=%v: registration.stopped became %v", startStopped, form, got)
			}
			var after int
			_ = d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'registration_%'`).Scan(&after)
			if after != before {
				t.Errorf("stopped=%v form=%v: a refused request wrote an audit row", startStopped, form)
			}
		}
	}
}
