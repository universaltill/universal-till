package pages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#1294: the Tills page's main-till row shows the same "Till ID" the
// LAN scan prints for that main till.

func TestMainTillDiscoveryID_MainTillMintsAndIsStable(t *testing.T) {
	d := newDiscoveryAPITestDeps(t)
	ctx := context.Background()
	repo := data.NewSettingsRepo(d.Db)

	got := mainTillDiscoveryID(ctx, d, "")
	if got == "" {
		t.Fatal("main till: want a minted discovery id, got blank")
	}
	stored, ok, err := repo.Get(ctx, discovery.TillIDSettingKey)
	if err != nil || !ok || stored != got {
		t.Fatalf("lan_discovery.till_id = %q ok=%v err=%v, want %q persisted", stored, ok, err, got)
	}
	if again := mainTillDiscoveryID(ctx, d, ""); again != got {
		t.Fatalf("second call = %q, want stable %q", again, got)
	}
	if adv, err := discovery.TillID(ctx, repo); err != nil || adv != got {
		t.Fatalf("advertised id = %q err=%v, want %q", adv, err, got)
	}
}

func TestMainTillDiscoveryID_JoinedTillUsesPrimaryTillID(t *testing.T) {
	d := newDiscoveryAPITestDeps(t)
	ctx := context.Background()
	repo := data.NewSettingsRepo(d.Db)
	if err := repo.Set(ctx, discovery.PrimaryTillIDSettingKey, " P "); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, discovery.TillIDSettingKey, "L"); err != nil {
		t.Fatal(err)
	}
	if got := mainTillDiscoveryID(ctx, d, "https://main.lan"); got != "P" {
		t.Fatalf("joined till id = %q, want %q", got, "P")
	}
}

func TestMainTillDiscoveryID_JoinedTillBlankNeverFallsBackOrMints(t *testing.T) {
	d := newDiscoveryAPITestDeps(t)
	ctx := context.Background()
	repo := data.NewSettingsRepo(d.Db)

	// Shop-wide lan_discovery copy present: still must not be shown.
	if err := repo.Set(ctx, discovery.TillIDSettingKey, "L"); err != nil {
		t.Fatal(err)
	}
	if got := mainTillDiscoveryID(ctx, d, "https://main.lan"); got != "" {
		t.Fatalf("joined, no primary_till_id: got %q, want blank (no fallback to lan_discovery)", got)
	}

	// Absent entirely: must not be minted.
	d2 := newDiscoveryAPITestDeps(t)
	if got := mainTillDiscoveryID(ctx, d2, "https://main.lan"); got != "" {
		t.Fatalf("joined, nothing stored: got %q, want blank", got)
	}
	if _, ok, _ := data.NewSettingsRepo(d2.Db).Get(ctx, discovery.TillIDSettingKey); ok {
		t.Fatal("joined till must never mint lan_discovery.till_id")
	}
}

func TestMainTillDiscoveryID_ErrorIsBlank(t *testing.T) {
	d := newDiscoveryAPITestDeps(t)
	orig := discoveryTillID
	discoveryTillID = func(ctx context.Context, s discovery.SettingsReader) (string, error) {
		return "", errors.New("settings db unavailable")
	}
	t.Cleanup(func() { discoveryTillID = orig })
	if got := mainTillDiscoveryID(context.Background(), d, ""); got != "" {
		t.Fatalf("error path = %q, want blank", got)
	}
}

func TestTillsRosterRendersMainTillDiscoveryID(t *testing.T) {
	chdirRoot(t)
	initAuthTestI18n(t)
	const id = "0b1c2d3e-4f50-4a6b-8c7d-9e0f1a2b3c4d"
	render := func(m map[string]any) string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/ui/tills/roster", nil)
		httpx.RenderPartial("ui/partials/tills_roster.html", m)(rec, req)
		return rec.Body.String()
	}

	body := render(map[string]any{"PrimaryTillName": "Front", "MainTillDiscoveryID": id})
	for _, want := range []string{`data-testid="tills-main-discovery-id"`, `Till ID: <bdi dir="ltr">` + id, "(this till)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("main till row missing %q; body=%s", want, body)
		}
	}

	body = render(map[string]any{"PrimaryTillName": "Front", "SyncPrimary": "https://x", "MainTillDiscoveryID": id})
	if !strings.Contains(body, id) || !strings.Contains(body, "(the main till)") {
		t.Fatalf("joined row should show the primary's id; body=%s", body)
	}

	body = render(map[string]any{"PrimaryTillName": "Front", "SyncPrimary": "https://x", "MainTillDiscoveryID": ""})
	if !strings.Contains(body, `data-testid="tills-main-discovery-id"`) || !strings.Contains(body, "Till ID: —") {
		t.Fatalf("blank id should render the label with a dash; body=%s", body)
	}
	if strings.Contains(body, "<bdi") {
		t.Fatalf("blank id must not render a bdi; body=%s", body)
	}
}

// Tester (ut-docs#1294): the real /ui/tills/roster handler on a real
// migrated DB wires the helper into the page — the template test above
// feeds the map by hand, so it can't catch tillsRosterData dropping the key.
func TestTillsRosterHandler_ShowsMainTillDiscoveryID(t *testing.T) {
	ctx := context.Background()

	// Main till: the row shows exactly the id the advertiser broadcasts.
	d := newDiscoveryAPITestDeps(t)
	mux := http.NewServeMux()
	registerTillsRoster(mux, d)
	rec := getRoster(t, mux, "manager")
	if rec.Code != http.StatusOK {
		t.Fatalf("roster = %d %s", rec.Code, rec.Body)
	}
	adv, err := discovery.TillID(ctx, data.NewSettingsRepo(d.Db))
	if err != nil || adv == "" {
		t.Fatalf("advertised id = %q err=%v", adv, err)
	}
	if want := `Till ID: <bdi dir="ltr">` + adv + `</bdi>`; !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("main till roster missing %q; body=%s", want, rec.Body)
	}

	// Joined till: the proven sync.primary_till_id, never the shop-wide copy.
	j := newDiscoveryAPITestDeps(t)
	repo := data.NewSettingsRepo(j.Db)
	for k, v := range map[string]string{
		"sync.primary_url":                "https://192.0.2.1:8080",
		discovery.PrimaryTillIDSettingKey: "11111111-2222-4333-8444-555555555555",
		discovery.TillIDSettingKey:        "99999999-8888-4777-8666-555555555555",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	jmux := http.NewServeMux()
	registerTillsRoster(jmux, j)
	body := getRoster(t, jmux, "manager").Body.String()
	if !strings.Contains(body, "11111111-2222-4333-8444-555555555555") || strings.Contains(body, "99999999-8888-4777-8666-555555555555") {
		t.Fatalf("joined roster: want primary_till_id and not lan_discovery; body=%s", body)
	}
}
