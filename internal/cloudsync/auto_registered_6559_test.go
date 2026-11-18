package cloudsync

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0200 (ut-docs#6559), owner decisions: a till whose cloud identity was
// made by the automatic register sends no shop name and no till name in its
// check-ins while unpaid; once paid, or once the operator claims/pairs it,
// the existing behaviour (names ride the report) returns.
func TestAutoRegisteredUnpaidTillSendsNoNames(t *testing.T) {
	cases := []struct {
		name      string
		autoReg   bool
		allowed   bool // paid (sync allowed)
		wantNames bool
	}{
		{"auto-registered and unpaid", true, false, false},
		{"auto-registered but paid", true, true, true},
		{"claimed or paired (not marked) and unpaid", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			settings := data.NewSettingsRepo(db)
			ctx := context.Background()
			if err := settings.Set(ctx, "till.name", "Front Counter"); err != nil {
				t.Fatal(err)
			}
			if tc.autoReg {
				if err := settings.Set(ctx, enroll.KeyAutoRegistered, "true"); err != nil {
					t.Fatal(err)
				}
			}
			prev := syncAllowedFn
			syncAllowedFn = func(context.Context, entitlement.Reader) bool { return tc.allowed }
			t.Cleanup(func() { syncAllowedFn = prev })
			hooks := Hooks{DeviceExtra: func(context.Context) map[string]any {
				return map[string]any{"store_name": "Corner Shop", "country": "GB"}
			}}
			dev := buildSyncRequest(ctx, testCfg(""), settings, hooks).devices[0]
			_, hasName := dev["name"]
			_, hasStore := dev["store_name"]
			if hasName != tc.wantNames || hasStore != tc.wantNames {
				t.Fatalf("name present=%v store_name present=%v, want both %v", hasName, hasStore, tc.wantNames)
			}
			if dev["country"] != "GB" {
				t.Errorf("a non-name field was dropped: %v", dev)
			}
		})
	}
}

// The start-up check-in of an auto-registered till obeys the same guards as
// register (dev build, demo, never-register, country, notice); a till that was
// not auto-registered keeps today's behaviour.
func TestStartupCheckinOfAutoRegisteredTillHonoursTheRegisterGuards(t *testing.T) {
	cases := []struct {
		name    string
		autoReg bool
		mutate  func(db *data.SettingsRepo)
		version string
		cfgFn   func(c *config.Config)
		want    bool // makes a request
	}{
		{"all guards pass", true, func(*data.SettingsRepo) {}, "0.30.20", nil, true},
		{"notice not shown", true, func(s *data.SettingsRepo) { _ = s.Set(context.Background(), enroll.KeyNoticeShownAt, "") }, "0.30.20", nil, false},
		{"Iran", true, func(s *data.SettingsRepo) { _ = s.Set(context.Background(), enroll.StoreCountrySettingsKey, "IR") }, "0.30.20", nil, false},
		{"dev build", true, func(*data.SettingsRepo) {}, "dev", nil, false},
		{"never register", true, func(*data.SettingsRepo) {}, "0.30.20", func(c *config.Config) { c.NeverRegister = true }, false},
		{"demo", true, func(*data.SettingsRepo) {}, "0.30.20", func(c *config.Config) { c.Demo = true }, false},
		{"not auto-registered keeps existing behaviour (no notice needed)", false, func(s *data.SettingsRepo) { _ = s.Set(context.Background(), enroll.KeyNoticeShownAt, "") }, "0.30.20", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cloud, srv, db, _ := startupFixture(t, http.StatusPaymentRequired)
			pinVersion(t, tc.version)
			settings := data.NewSettingsRepo(db)
			if err := settings.SetMany(context.Background(), map[string]string{
				"setup.completed":       "true",
				enroll.KeyNoticeShownAt: time.Now().UTC().Format(time.RFC3339),
				enroll.KeyNoticeVersion: enroll.NoticeVersion,
			}); err != nil {
				t.Fatal(err)
			}
			if tc.autoReg {
				_ = settings.Set(context.Background(), enroll.KeyAutoRegistered, "true")
			}
			tc.mutate(settings)
			cfg := testCfg(srv.URL)
			if tc.cfgFn != nil {
				tc.cfgFn(cfg)
			}
			armStartup()
			_, _ = tick(context.Background(), cfg, db, Hooks{}) // the cloud answers 402: an error by design
			if got := len(cloud.requests()) > 0; got != tc.want {
				t.Fatalf("made a request = %v, want %v (requests %v)", got, tc.want, cloud.requests())
			}
		})
	}
}
