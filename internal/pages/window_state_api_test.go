package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /api/window-mode (ut-docs#611) is the desktop shell's own read of the
// persisted window-mode + launch-on-startup preferences, queried at launch
// before any operator has signed in — it must work unauthenticated (mirrors
// /healthz) and reflect whatever's currently in runtime state. The shell,
// not this handler, applies either preference (see settings_page.go's
// launch-on-startup handler comment for why that split is deliberate).
func TestWindowStateAPI_UnauthenticatedReturnsPersistedMode(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	st := d.CurrentState()
	st.WindowMode = "kiosk"
	st.LaunchOnStartup = true
	d.SetState(st)

	req := httptest.NewRequest(http.MethodGet, "/api/window-mode", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/window-mode = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			WindowMode      string `json:"window_mode"`
			LaunchOnStartup bool   `json:"launch_on_startup"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	if body.Data.WindowMode != "kiosk" {
		t.Fatalf("window_mode = %q, want kiosk", body.Data.WindowMode)
	}
	if !body.Data.LaunchOnStartup {
		t.Fatal("launch_on_startup = false, want true")
	}
	if body.Error != nil {
		t.Fatalf("error = %v, want nil", body.Error)
	}
}

func TestWindowStateAPI_DefaultsToNormalAndStartupOff(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/api/window-mode", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body struct {
		Data struct {
			WindowMode      string `json:"window_mode"`
			LaunchOnStartup bool   `json:"launch_on_startup"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.WindowMode != "normal" {
		t.Fatalf("window_mode = %q, want normal (common.DefaultWindowMode)", body.Data.WindowMode)
	}
	if body.Data.LaunchOnStartup {
		t.Fatal("launch_on_startup = true, want false (zero value)")
	}
}
