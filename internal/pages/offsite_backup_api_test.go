package pages

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/offsitebackup"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5066: the till side of the off-site backup. A cashier gets no
// control and every route refuses her; a manager sees the card and the
// restore page; restoring asks for the manager PIN before anything starts.

type fakeOffsiteCloud struct {
	offsitebackup.Cloud
	list     offsitebackup.ListResponse
	started  int
	keyState offsitebackup.KeyState
	puts     []offsitebackup.KeyPut
	putErr   error
}

func (f *fakeOffsiteCloud) List(context.Context) (offsitebackup.ListResponse, error) {
	return f.list, nil
}
func (f *fakeOffsiteCloud) RequestRestore(context.Context, string, *offsitebackup.HaveKey) (offsitebackup.RestoreRequestResponse, error) {
	f.started++
	return offsitebackup.RestoreRequestResponse{}, io.EOF
}
func (f *fakeOffsiteCloud) GetKey(context.Context) (offsitebackup.KeyState, error) {
	return f.keyState, nil
}
func (f *fakeOffsiteCloud) PutKey(_ context.Context, k offsitebackup.KeyPut) error {
	f.puts = append(f.puts, k)
	return f.putErr
}
func (f *fakeOffsiteCloud) RequestKeyLink(context.Context) (offsitebackup.RestoreRequestResponse, error) {
	f.started++
	return offsitebackup.RestoreRequestResponse{}, io.EOF
}

func offsiteMux(t *testing.T, entitled bool) (*http.ServeMux, *common.Deps, *fakeOffsiteCloud) {
	t.Helper()
	mux, _, d := newFullAuthDeps(t)
	d.Cfg.DBPath = filepath.Join(t.TempDir(), "unitill-pos.db")
	d.Cfg.Marketplace.EndpointURL = "https://cloud.example.test"
	d.Cfg.Marketplace.StoreID = "st_1"
	d.Cfg.Marketplace.MerchantToken = "tok"
	ctx := context.Background()
	if entitled {
		now := time.Now().UTC().Format(time.RFC3339)
		for k, v := range map[string]string{entitlement.KeyPlan: string(entitlement.PlanLocalPlus),
			entitlement.KeySubscriptionStatus: entitlement.StatusActive, entitlement.KeyLastConfirmedAt: now} {
			if err := d.Settings.Set(ctx, k, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	registerOffsiteBackup(mux, d)
	registerBackupCopyChip(mux, d)
	fc := &fakeOffsiteCloud{list: offsitebackup.ListResponse{Entitled: true, Backups: []offsitebackup.BackupEntry{
		{BackupID: "6f1ca001-1122-4344-8566-778899aabbcc", TakenAt: "2026-11-15T02:00:05Z", CiphertextBytes: 3 << 20, Tier: "daily"},
	}}}
	svc := offsitebackup.Shared(d.Cfg, d.Db)
	svc.Cloud = fc
	svc.Uploader.Cloud = fc
	svc.Uploader.Keys = offsitebackup.KeyStore{Dir: filepath.Join(t.TempDir(), "keys")}
	svc.Restorer.Keys = svc.Uploader.Keys
	return mux, d, fc
}

func postAs(mux *http.ServeMux, path string, form url.Values, u *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestOffsite_CashierGetsNoControlAndEveryRouteRefuses(t *testing.T) {
	mux, _, fc := offsiteMux(t, true)
	// Hidden partials stay empty for a cashier.
	for _, p := range []string{"/ui/backup-cloud-card", "/ui/backup-cloud-restore/status", "/ui/offsite-nextstep"} {
		if rec := getAs(mux, p, &cashUser); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "" {
			t.Errorf("GET %s as cashier = %d %q, want empty 200", p, rec.Code, rec.Body.String())
		}
	}
	// The page and the mutating routes refuse on the server.
	for _, p := range []string{"/backups/cloud-restore", "/backups/cloud-key"} {
		if rec := getAs(mux, p, &cashUser); rec.Code != http.StatusForbidden {
			t.Errorf("%s as cashier = %d, want 403", p, rec.Code)
		}
	}
	for _, p := range []string{"/api/offsite/recover/start", "/api/offsite/recover/cancel", "/api/offsite/link/start",
		"/api/offsite/key/choice", "/api/offsite/key/new", "/api/offsite/key/enter", "/api/offsite/recover/key", "/api/offsite/key/show", "/api/offsite/key/print"} {
		if rec := postAs(mux, p, url.Values{"backup_id": {"6f1ca001-1122-4344-8566-778899aabbcc"}}, &cashUser); rec.Code != http.StatusForbidden {
			t.Errorf("POST %s as cashier = %d, want 403", p, rec.Code)
		}
	}
	// Upload now: a cashier gets the PIN prompt, never the action.
	rec := postAs(mux, "/api/offsite/now", nil, &cashUser)
	if strings.Contains(rec.Body.String(), "backup-cloud-queued") || !strings.Contains(rec.Body.String(), "override_pin") {
		t.Errorf("cashier upload-now = %d %q", rec.Code, rec.Body.String())
	}
	if fc.started != 0 {
		t.Error("a restore was requested for a cashier")
	}
}

func TestOffsite_ManagerSeesCardInEachState(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	ctx := context.Background()
	body := func() string { return getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String() }

	if b := body(); !strings.Contains(b, `data-state="waiting"`) || !strings.Contains(b, "backup-cloud-now") || !strings.Contains(b, "/backups/cloud-restore") {
		t.Fatalf("waiting card: %s", b)
	}
	_ = d.Settings.Set(ctx, data.OffsiteLastSuccessAtKey, time.Now().UTC().Format(time.RFC3339))
	if b := body(); !strings.Contains(b, `data-state="ok"`) || !strings.Contains(b, "Last copied to the cloud") {
		t.Fatalf("ok card: %s", b)
	}
	_ = d.Settings.Set(ctx, data.OffsiteFailingSinceKey, time.Now().UTC().Format(time.RFC3339))
	_ = d.Settings.Set(ctx, data.OffsiteLastErrorKey, "disk")
	if b := body(); !strings.Contains(b, `data-state="failing"`) || !strings.Contains(b, "not enough free space") {
		t.Fatalf("failing card: %s", b)
	}
	_ = data.NewSettingsRepo(d.Db).Delete(ctx, data.OffsiteFailingSinceKey)
	_ = d.Settings.Set(ctx, data.OffsiteRefusedReasonKey, "cloud_backup_not_in_plan")
	if b := body(); !strings.Contains(b, `data-state="refused"`) || !strings.Contains(b, "does not include cloud backups") {
		t.Fatalf("refused card: %s", b)
	}
}

func TestOffsite_UnentitledAndAdditionalTillShowNoUploadButton(t *testing.T) {
	mux, d, _ := offsiteMux(t, false)
	b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
	if !strings.Contains(b, `data-state="not_in_plan"`) || strings.Contains(b, "backup-cloud-now") {
		t.Fatalf("unentitled card: %s", b)
	}
	// An additional till (sync.primary_url) has no card at all.
	_ = d.Settings.Set(context.Background(), "sync.primary_url", "http://192.168.1.5:8080")
	if b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String(); strings.TrimSpace(b) != "" {
		t.Fatalf("additional till shows a card: %s", b)
	}
	if rec := getAs(mux, "/ui/offsite-nextstep", &mgrUser); strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("additional till shows the next step")
	}
}

func TestOffsite_RestorePageListsBackupsAndWarnsAboutSales(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	rec := getAs(mux, "/backups/cloud-restore", &mgrUser)
	b := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, "cloud-restore-list") || !strings.Contains(b, "Sales after") || !strings.Contains(b, "6f1ca001-1122-4344-8566-778899aabbcc") {
		t.Fatalf("restore page = %d: %s", rec.Code, b)
	}
	if strings.Contains(b, "cloud-restore-has-sales") {
		t.Error("sales warning shown on an empty till")
	}
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, status, subtotal, total, created_at) VALUES ('s1', 'R-1', 'completed', 250, 250, '2026-11-01 10:00:00')`); err != nil {
		t.Fatal(err)
	}
	if b := getAs(mux, "/backups/cloud-restore", &mgrUser).Body.String(); !strings.Contains(b, "cloud-restore-has-sales") || !strings.Contains(b, "already has 1 sales") {
		t.Fatalf("sales warning missing: %s", b)
	}
}

func TestOffsite_RestoreNeedsThePINAndAListedBackup(t *testing.T) {
	mux, _, fc := offsiteMux(t, true)
	id := "6f1ca001-1122-4344-8566-778899aabbcc"
	// No PIN: the elevation prompt, nothing requested.
	rec := postAs(mux, "/api/offsite/recover/start", url.Values{"backup_id": {id}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "override_pin") || fc.started != 0 {
		t.Fatalf("no-PIN start = %d %q started=%d", rec.Code, rec.Body.String(), fc.started)
	}
	// A wrong PIN: still nothing.
	rec = postAs(mux, "/api/offsite/recover/start", url.Values{"backup_id": {id}, "override_pin": {"000000"}}, &mgrUser)
	if fc.started != 0 {
		t.Fatalf("wrong PIN started a restore: %q", rec.Body.String())
	}
	// An id the cloud does not list is refused before the PIN.
	rec = postAs(mux, "/api/offsite/recover/start", url.Values{"backup_id": {"nope"}}, &mgrUser)
	if rec.Code != http.StatusConflict || fc.started != 0 {
		t.Fatalf("unlisted id = %d started=%d", rec.Code, fc.started)
	}
}

func TestOffsite_RestoreStatusRendersEachPhase(t *testing.T) {
	cases := map[string][]string{
		offsitebackup.PhaseWaiting:     {"cloud-restore-code", "K7QX29PM", "hx-trigger=\"every 3s\"", "cloud-restore-cancel"},
		offsitebackup.PhaseDownloading: {"Downloading 2 of 5", "<progress"},
		offsitebackup.PhaseReady:       {"cloud-restore-ready", "backup-restart-btn"},
		offsitebackup.PhaseFailed:      {"cloud-restore-failed", "belongs to another shop"},
		offsitebackup.PhaseDenied:      {"declined"},
	}
	for phase, want := range cases {
		st := offsitebackup.RestoreState{Phase: phase, Code: "K7QX29PM", Done: 2, Total: 5, ErrCode: "other_shop", ApproveURL: "https://my.example.test/en-gb/restore?code=K7QX29PM"}
		old := backupRestartSupported
		backupRestartSupported = func() bool { return true }
		rec := httptest.NewRecorder()
		renderRestoreStatus(rec, httptest.NewRequest(http.MethodGet, "/", nil), st)
		backupRestartSupported = old
		for _, w := range want {
			if !strings.Contains(rec.Body.String(), w) {
				t.Errorf("%s: missing %q in %s", phase, w, rec.Body.String())
			}
		}
		if phase == offsitebackup.PhaseWaiting && !strings.Contains(rec.Body.String(), "data:image/png;base64,") {
			t.Errorf("waiting: no QR")
		}
		if phase == offsitebackup.PhaseReady && strings.Contains(rec.Body.String(), "every 3s") {
			t.Error("ready phase still polls")
		}
	}
}

func TestOffsite_ChipShowsAfter48HoursOfFailureOnly(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	ctx := context.Background()
	chip := func() string { return getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String() }
	_ = d.Settings.Set(ctx, data.OffsiteFailingSinceKey, time.Now().Add(-47*time.Hour).UTC().Format(time.RFC3339))
	if b := chip(); strings.Contains(b, "sb-backup-cloud-failing") {
		t.Fatalf("chip at 47 h: %s", b)
	}
	_ = d.Settings.Set(ctx, data.OffsiteFailingSinceKey, time.Now().Add(-49*time.Hour).UTC().Format(time.RFC3339))
	b := chip()
	if !strings.Contains(b, "sb-backup-cloud-failing") || !strings.Contains(b, "Cloud backup failing") || strings.Contains(b, "<dialog") {
		t.Fatalf("chip at 49 h: %s", b)
	}
	if got := getAs(mux, "/ui/backup-copy-chip", &cashUser).Body.String(); strings.TrimSpace(got) != "" {
		t.Fatalf("cashier sees the cloud chip: %q", got)
	}
	// A refusal is not a failure: no failing_since, no chip.
	_ = data.NewSettingsRepo(d.Db).Delete(ctx, data.OffsiteFailingSinceKey)
	_ = d.Settings.Set(ctx, data.OffsiteRefusedReasonKey, "subscription_inactive")
	if b := chip(); strings.Contains(b, "sb-backup-cloud-failing") {
		t.Fatalf("a refusal shows as failing: %s", b)
	}
}

func TestOffsite_NeedsLinkCardAndLinkNeedsManagerPIN(t *testing.T) {
	mux, d, fc := offsiteMux(t, true)
	ctx := context.Background()
	_ = d.Settings.Set(ctx, data.OffsiteRefusedReasonKey, offsitebackup.ReasonNeedsLink)
	b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
	if !strings.Contains(b, `data-state="needs_link"`) || !strings.Contains(b, "backup-cloud-link") || !strings.Contains(b, "Link this till to your backup key") {
		t.Fatalf("needs-link card: %s", b)
	}
	// A cashier never sees the card or the action.
	if got := getAs(mux, "/ui/backup-cloud-card", &cashUser).Body.String(); strings.TrimSpace(got) != "" {
		t.Fatalf("cashier sees the link card: %q", got)
	}
	// No PIN: the prompt, nothing requested; wrong PIN: nothing requested.
	rec := postAs(mux, "/api/offsite/link/start", nil, &mgrUser)
	if !strings.Contains(rec.Body.String(), "override_pin") || fc.started != 0 {
		t.Fatalf("no-PIN link = %d %q started=%d", rec.Code, rec.Body.String(), fc.started)
	}
	postAs(mux, "/api/offsite/link/start", url.Values{"override_pin": {"000000"}}, &mgrUser)
	if fc.started != 0 {
		t.Fatal("a wrong PIN requested the key")
	}
}

func TestOffsite_LinkStatusShowsKeyWordingAndDone(t *testing.T) {
	render := func(st offsitebackup.RestoreState) string {
		rec := httptest.NewRecorder()
		renderRestoreStatus(rec, httptest.NewRequest(http.MethodGet, "/", nil), st)
		return rec.Body.String()
	}
	w := render(offsitebackup.RestoreState{Kind: offsitebackup.KindLink, Phase: offsitebackup.PhaseWaiting, Code: "LINK2X9P", ApproveURL: "https://my.example.test/x?code=LINK2X9P"})
	if !strings.Contains(w, "LINK2X9P") || !strings.Contains(w, "backup key") || !strings.Contains(w, "data:image/png;base64,") || !strings.Contains(w, "every 3s") {
		t.Errorf("waiting: %s", w)
	}
	if d := render(offsitebackup.RestoreState{Kind: offsitebackup.KindLink, Phase: offsitebackup.PhaseLinked}); !strings.Contains(d, "cloud-link-done") || strings.Contains(d, "every 3s") {
		t.Errorf("linked: %s", d)
	}
}

const offsitePIN = "135790"

// withManagerPIN gives the test manager a real PIN so the step-up can pass.
func withManagerPIN(t *testing.T, d *common.Deps) {
	t.Helper()
	hash, err := auth.HashPIN(offsitePIN)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.NewAuthRepo(d.Db).SetUserPIN(context.Background(), mgrUser.ID, hash); err != nil {
		t.Fatal(err)
	}
}

func TestOffsite_KeyChoiceAsksOncePINGatedAndCallsTheCloud(t *testing.T) {
	mux, d, fc := offsiteMux(t, true)
	withManagerPIN(t, d)
	ctx := context.Background()
	svc := offsitebackup.Shared(d.Cfg, d.Db)
	dek, gen, err := svc.Uploader.Keys.Generate("st_1")
	if err != nil {
		t.Fatal(err)
	}
	fp := offsitebackup.Fingerprint(dek)
	fc.keyState = offsitebackup.KeyState{Generation: &gen, Fingerprint: &fp}

	// Unasked: the card asks, with the trade-off, and offers Save your key.
	b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
	for _, w := range []string{"cloud-key-ask", "technically decrypt", "cannot be read by anyone", "cloud-key-choose-on", "cloud-key-choose-off", "cloud-key-save-link"} {
		if !strings.Contains(b, w) {
			t.Errorf("unasked card misses %q", w)
		}
	}
	// A cashier never sees any of it.
	if got := getAs(mux, "/ui/backup-cloud-card", &cashUser).Body.String(); strings.TrimSpace(got) != "" {
		t.Fatalf("cashier sees the key question: %q", got)
	}
	// No PIN: prompt only, nothing stored.
	rec := postAs(mux, "/api/offsite/key/choice", url.Values{"choice": {"on"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "override_pin") {
		t.Fatalf("no prompt: %s", rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(ctx, data.OffsiteKeyInCloudKey); v != "" {
		t.Fatalf("choice stored without a PIN: %q", v)
	}
	if rec := postAs(mux, "/api/offsite/key/choice", url.Values{"choice": {"maybe"}, "override_pin": {offsitePIN}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid choice = %d", rec.Code)
	}
	// On with the PIN: stored, the key is sent for wrapping.
	rec = postAs(mux, "/api/offsite/key/choice", url.Values{"choice": {"on"}, "override_pin": {offsitePIN}}, &mgrUser)
	if v, _, _ := d.Settings.Get(ctx, data.OffsiteKeyInCloudKey); v != "on" {
		t.Fatalf("choice = %q (%d %s)", v, rec.Code, rec.Body.String())
	}
	if len(fc.puts) != 1 || fc.puts[0].DEKB64 == "" || fc.puts[0].Fingerprint != fp {
		t.Fatalf("puts = %+v", fc.puts)
	}
	if body := rec.Body.String(); strings.Contains(body, "cloud-key-ask") || !strings.Contains(body, "cloud-key-status") {
		t.Errorf("card after choosing: %s", body)
	}
	// Off is stored, but the till never deletes a copy the cloud holds (M3).
	fc.keyState.InCloud = true
	postAs(mux, "/api/offsite/key/choice", url.Values{"choice": {"off"}, "override_pin": {offsitePIN}}, &mgrUser)
	if v, _, _ := d.Settings.Get(ctx, data.OffsiteKeyInCloudKey); v != "off" {
		t.Fatalf("choice = %q, want off", v)
	}
}

func TestOffsite_KeyChipAsksOnlyManagersUntilChosen(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	svc := offsitebackup.Shared(d.Cfg, d.Db)
	chip := func(u *auth.User) string { return getAs(mux, "/ui/backup-copy-chip", u).Body.String() }
	if strings.Contains(chip(&mgrUser), "sb-backup-key-ask") {
		t.Fatal("chip before the till has a key")
	}
	_, _, _ = svc.Uploader.Keys.Generate("st_1")
	if b := chip(&mgrUser); !strings.Contains(b, "sb-backup-key-ask") || !strings.Contains(b, "Backup key choice") || strings.Contains(b, "<dialog") {
		t.Fatalf("chip: %s", b)
	}
	if got := chip(&cashUser); strings.TrimSpace(got) != "" {
		t.Fatalf("cashier sees the key chip: %q", got)
	}
	_ = d.Settings.Set(context.Background(), data.OffsiteKeyInCloudKey, "off")
	if strings.Contains(chip(&mgrUser), "sb-backup-key-ask") {
		t.Fatal("chip after the choice")
	}
}

func TestOffsite_BlockedByOwnerShowsItAndHidesTheSwitch(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	_, _, _ = offsitebackup.Shared(d.Cfg, d.Db).Uploader.Keys.Generate("st_1")
	ctx := context.Background()
	_ = d.Settings.Set(ctx, data.OffsiteKeyInCloudKey, "on")
	_ = d.Settings.Set(ctx, data.OffsiteEscrowBlockedKey, "1")
	b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
	if !strings.Contains(b, "Turned off by the owner on my.universaltill.com.") || strings.Contains(b, "cloud-key-change") {
		t.Fatalf("blocked card: %s", b)
	}
}

func TestOffsite_StuckShopCardOffersNewKeyOnlyWhenThereIsNoCloudCopy(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	ctx := context.Background()
	_ = d.Settings.Set(ctx, data.OffsiteRefusedReasonKey, offsitebackup.ReasonNeedsLink)
	_ = d.Settings.Set(ctx, data.OffsiteLinkCopyKey, "no")
	b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
	for _, w := range []string{"cloud-key-new", "Backups made before today can only be opened with the key you saved.", "cloud-key-enter-input"} {
		if !strings.Contains(b, w) {
			t.Errorf("no-copy card misses %q", w)
		}
	}
	if strings.Contains(b, "backup-cloud-link") {
		t.Error("the my. link is offered although there is no cloud copy")
	}
	_ = d.Settings.Set(ctx, data.OffsiteLinkCopyKey, "yes")
	b = getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
	if !strings.Contains(b, "backup-cloud-link") || strings.Contains(b, "cloud-key-new") || !strings.Contains(b, "cloud-key-enter-input") {
		t.Errorf("cloud-copy card: link=%v new=%v enter=%v", strings.Contains(b, "backup-cloud-link"), strings.Contains(b, "cloud-key-new"), strings.Contains(b, "cloud-key-enter-input"))
	}
}

func TestOffsite_StartNewKeyNeedsPINAndMakesGenerationTwo(t *testing.T) {
	mux, d, fc := offsiteMux(t, true)
	withManagerPIN(t, d)
	one := 1
	fp := "00112233445566778899aabbccddeeff"
	fc.keyState = offsitebackup.KeyState{Generation: &one, Fingerprint: &fp}
	if rec := postAs(mux, "/api/offsite/key/new", nil, &mgrUser); !strings.Contains(rec.Body.String(), "override_pin") || len(fc.puts) != 0 {
		t.Fatalf("no-PIN: %s puts=%d", rec.Body.String(), len(fc.puts))
	}
	rec := postAs(mux, "/api/offsite/key/new", url.Values{"override_pin": {offsitePIN}}, &mgrUser)
	if len(fc.puts) != 1 || fc.puts[0].Generation != 2 {
		t.Fatalf("puts = %+v (%d %s)", fc.puts, rec.Code, rec.Body.String())
	}
	if _, g, err := offsitebackup.Shared(d.Cfg, d.Db).Uploader.Keys.Load("st_1"); err != nil || g != 2 {
		t.Fatalf("key file: %d %v", g, err)
	}
}

func TestOffsite_EnterSavedKeyChecksTheKey(t *testing.T) {
	mux, d, fc := offsiteMux(t, true)
	withManagerPIN(t, d)
	dek := make([]byte, offsitebackup.KeySize)
	dek[0] = 1
	one, fp := 1, offsitebackup.Fingerprint(dek)
	fc.keyState = offsitebackup.KeyState{Generation: &one, Fingerprint: &fp}
	bad := postAs(mux, "/api/offsite/key/enter", url.Values{"saved_key": {"UTK1-ABCD"}, "override_pin": {offsitePIN}}, &mgrUser)
	if !strings.Contains(bad.Body.String(), "not a complete saved key") {
		t.Fatalf("bad key: %s", bad.Body.String())
	}
	other := make([]byte, offsitebackup.KeySize)
	wrong := postAs(mux, "/api/offsite/key/enter", url.Values{"saved_key": {offsitebackup.EncodeRecovery(1, other)}, "override_pin": {offsitePIN}}, &mgrUser)
	if !strings.Contains(wrong.Body.String(), "does not belong to this shop") {
		t.Fatalf("wrong key: %s", wrong.Body.String())
	}
	if _, _, err := offsitebackup.Shared(d.Cfg, d.Db).Uploader.Keys.Load("st_1"); err == nil {
		t.Fatal("a rejected key was saved")
	}
	ok := postAs(mux, "/api/offsite/key/enter", url.Values{"saved_key": {offsitebackup.EncodeRecovery(1, dek)}, "override_pin": {offsitePIN}}, &mgrUser)
	if !strings.Contains(ok.Body.String(), "now has your backup key") {
		t.Fatalf("good key: %s", ok.Body.String())
	}
	if got, _, err := offsitebackup.Shared(d.Cfg, d.Db).Uploader.Keys.Load("st_1"); err != nil || string(got) != string(dek) {
		t.Fatalf("not saved: %v", err)
	}
}

func TestOffsite_ShowKeyNeedsPINIsNoStoreAndRoundTrips(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	withManagerPIN(t, d)
	svc := offsitebackup.Shared(d.Cfg, d.Db)
	dek, gen, _ := svc.Uploader.Keys.Generate("st_1")
	// The page itself never contains the key.
	page := getAs(mux, "/backups/cloud-key", &mgrUser)
	if page.Code != 200 || strings.Contains(page.Body.String(), "UTK1-") || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("page: %d %q", page.Code, page.Header().Get("Cache-Control"))
	}
	if rec := postAs(mux, "/api/offsite/key/show", nil, &mgrUser); strings.Contains(rec.Body.String(), "UTK1-") {
		t.Fatalf("the key appeared without a PIN")
	}
	rec := postAs(mux, "/api/offsite/key/show", url.Values{"override_pin": {offsitePIN}}, &mgrUser)
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	i := strings.Index(body, "UTK1-")
	if i < 0 {
		t.Fatalf("no key shown: %s", body)
	}
	end := strings.Index(body[i:], "<")
	g, got, err := offsitebackup.DecodeRecovery(body[i : i+end])
	if err != nil || g != gen || string(got) != string(dek) {
		t.Fatalf("shown key does not round-trip: %v", err)
	}
	if !strings.Contains(body, "cloud-key-download") || !strings.Contains(body, `data-action="print"`) || !strings.Contains(body, "data:text/plain") {
		t.Errorf("print/download missing: %s", body)
	}
	if v, _, _ := d.Settings.Get(context.Background(), data.OffsiteKeyExportedAtKey); v == "" {
		t.Error("key_exported_at not recorded")
	}
	// The printer is off in tests: a plain message, never the key.
	pr := postAs(mux, "/api/offsite/key/print", url.Values{"override_pin": {offsitePIN}}, &mgrUser)
	if strings.Contains(pr.Body.String(), "UTK1-") || !strings.Contains(pr.Body.String(), "printer is off") {
		t.Errorf("print = %s", pr.Body.String())
	}
}

func TestOffsite_KeyPrintLinesStayWithinTheReceiptWidth(t *testing.T) {
	key := offsitebackup.EncodeRecovery(1, make([]byte, offsitebackup.KeySize))
	lines := keyPrintLines(key)
	if len(lines) < 2 {
		t.Fatalf("lines: %d", len(lines))
	}
	var joined []string
	for _, l := range lines {
		if len(l.Name) > 40 {
			t.Errorf("line too wide: %q", l.Name)
		}
		joined = append(joined, l.Name)
	}
	if strings.Join(joined, "-") != key {
		t.Error("lines do not rebuild the key")
	}
}

func TestOffsite_NeedKeyPhaseShowsTheEntryFormAndDoesNotPoll(t *testing.T) {
	rec := httptest.NewRecorder()
	renderRestoreStatusMsg(rec, httptest.NewRequest(http.MethodGet, "/", nil), offsitebackup.RestoreState{Phase: offsitebackup.PhaseAskSaved}, "bad")
	b := rec.Body.String()
	for _, w := range []string{"cloud-restore-needkey", "Enter the backup key you saved", `type="file"`, "cloud-key-error", "cloud-restore-cancel"} {
		if !strings.Contains(b, w) {
			t.Errorf("missing %q in %s", w, b)
		}
	}
	if strings.Contains(b, "every 3s") {
		t.Error("the entry form must not be replaced by a poll")
	}
}

// ut-docs#6607: the cloud refuses a new generation without the owner's approval
// on my.; the till says whom to ask and keeps no key.
func TestOffsite_StartNewKeyWithoutOwnerApprovalSaysAskTheOwner(t *testing.T) {
	mux, d, fc := offsiteMux(t, true)
	withManagerPIN(t, d)
	one := 1
	fp := "00112233445566778899aabbccddeeff"
	fc.keyState = offsitebackup.KeyState{Generation: &one, Fingerprint: &fp}
	fc.putErr = &offsitebackup.APIError{Status: 403, Code: "new_key_not_approved"}
	rec := postAs(mux, "/api/offsite/key/new", url.Values{"override_pin": {offsitePIN}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "approve a new key on my.universaltill.com") {
		t.Fatalf("no owner-approval wording: %s", rec.Body.String())
	}
	if _, _, err := offsitebackup.Shared(d.Cfg, d.Db).Uploader.Keys.Load("st_1"); err == nil {
		t.Fatal("the till kept a key the cloud refused")
	}
}

// ut-docs#7058: a dead device credential says what to do on the card.

func TestCardReasonKey(t *testing.T) {
	cases := []struct {
		reason   string
		hasOwner bool
		key      string
		link     bool
	}{
		{"pair_again", true, "backup.cloud.err.pair_again", true},
		{"pair_again", false, "backup.cloud.err.register_new", true},
		{"till_replaced", true, "backup.cloud.err.till_replaced", false},
		{"till_moved", true, "backup.cloud.err.till_moved", true},
		{"till_removed", false, "backup.cloud.err.till_removed", true},
		{"service_unavailable", true, "backup.cloud.err.service_unavailable", false},
		{"owner_approval", true, "backup.cloud.key.err.owner_approval", false},
		{"disk", true, "backup.cloud.err.disk", false},
		{"other", true, "backup.cloud.err.upload_other", false},
		{"", true, "backup.cloud.err.upload_other", false},
		{"bogus", true, "backup.cloud.err.upload_other", false},
	}
	for _, c := range cases {
		key, link := cardReasonKey(c.reason, c.hasOwner)
		if key != c.key || link != c.link {
			t.Errorf("cardReasonKey(%q, %v) = %q, %v; want %q, %v", c.reason, c.hasOwner, key, link, c.key, c.link)
		}
		if httpx.T("en", key) == key {
			t.Errorf("key %q is not in en.json", key)
		}
	}
}

func TestOffsite_CardCredentialRefusalSaysWhatToDo(t *testing.T) {
	mux, d, _ := offsiteMux(t, true)
	ctx := context.Background()
	_ = d.Settings.Set(ctx, data.OffsiteRefusedReasonKey, "pair_again")
	_ = d.Settings.Set(ctx, data.OffsiteFailingSinceKey, time.Now().UTC().Format(time.RFC3339))
	_ = d.Settings.Set(ctx, entitlement.KeyStoreClaimed, "true")
	b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String()
	if !strings.Contains(b, `data-state="refused"`) || !strings.Contains(b, "no longer accepts this till") ||
		!strings.Contains(b, `data-testid="backup-cloud-reason-link"`) || !strings.Contains(b, `href="/settings#registration"`) ||
		!strings.Contains(b, `class="set-row set-below"><a class="btn secondary"`) {
		t.Fatalf("owned store card: %s", b)
	}
	if strings.Contains(b, "Something went wrong") {
		t.Fatalf("falls back to the restore line: %s", b)
	}
	_ = d.Settings.Set(ctx, entitlement.KeyStoreClaimed, "false")
	if b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String(); !strings.Contains(b, "Reconnect the till") {
		t.Fatalf("anonymous store card: %s", b)
	}
	// A replaced till has no card.
	_ = d.Settings.Set(ctx, "till_identity.replaced", "1")
	if b := getAs(mux, "/ui/backup-cloud-card", &mgrUser).Body.String(); strings.TrimSpace(b) != "" {
		t.Fatalf("replaced till shows a card: %s", b)
	}
}

func TestOffsite_CloudFailingChipHiddenWhenNotMainReplacedOrRefused(t *testing.T) {
	for name, set := range map[string][2]string{
		"additional till": {"sync.primary_url", "http://192.168.1.5:8080"},
		"replaced":        {"till_identity.replaced", "1"},
		"refused":         {data.OffsiteRefusedReasonKey, "pair_again"},
	} {
		t.Run(name, func(t *testing.T) {
			mux, d, _ := offsiteMux(t, true)
			ctx := context.Background()
			_ = d.Settings.Set(ctx, data.OffsiteFailingSinceKey, time.Now().Add(-60*time.Hour).UTC().Format(time.RFC3339))
			if b := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); !strings.Contains(b, "sb-backup-cloud-failing") {
				t.Fatalf("control: chip missing before the condition: %s", b)
			}
			_ = d.Settings.Set(ctx, set[0], set[1])
			if b := getAs(mux, "/ui/backup-copy-chip", &mgrUser).Body.String(); strings.Contains(b, "sb-backup-cloud-failing") {
				t.Fatalf("chip shown: %s", b)
			}
		})
	}
}
