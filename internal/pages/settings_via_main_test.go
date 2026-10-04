package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ut-docs#2753: a replica its main till registered has no store token, but
// it is registered — the status bar and the Settings card must say so.
func TestSettingsAndStatusBar_ReplicaRegisteredViaMainTill(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, d.Settings, &sync.WaitGroup{}) })

	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	for k, v := range map[string]string{
		"sync.primary_url":              "http://127.0.0.1:1",
		"marketplace.device_id":         "till-replica-own",
		"marketplace.device_registered": "till-replica-own",
	} {
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	// Not vouched yet: the chip and Register now show.
	enroll.Init(ctx, &config.Config{}, emptyKV{}, &sync.WaitGroup{})
	body := get()
	if !strings.Contains(body, `sb-enrol`) || !strings.Contains(body, `hx-post="/api/enrol/now"`) {
		t.Fatalf("unregistered till: want the register chip and Register now")
	}

	// Vouched replica.
	enroll.Init(ctx, &config.Config{}, d.Settings, &sync.WaitGroup{})
	body = get()
	if strings.Contains(body, `sb-enrol`) {
		t.Errorf("status bar still shows the register chip on a vouched replica")
	}
	if !strings.Contains(body, "This till is registered through the main till.") {
		t.Errorf("settings card lacks the via-main text")
	}
	if !strings.Contains(body, "till-replica-own") {
		t.Errorf("settings card lacks the device id")
	}
	for _, bad := range []string{`hx-post="/api/enrol/now"`, `hx-post="/api/enrol/claim-code"`, `/api/enrol/devices`, "This till is not registered yet."} {
		if strings.Contains(body, bad) {
			t.Errorf("vouched replica card must not contain %q", bad)
		}
	}
}

// emptyKV is an enroll.Settings with nothing in it.
type emptyKV struct{}

func (emptyKV) Get(_ context.Context, _ string) (string, bool, error) { return "", false, nil }
func (emptyKV) Set(_ context.Context, _, _ string) error              { return nil }

// GetOrCreate stores nothing (Set is a no-op too): it hands back the default.
func (emptyKV) GetOrCreate(_ context.Context, _, def string) (string, error) { return def, nil }
