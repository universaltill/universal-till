package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
)

// ut-docs#2759: after "Update now" the status bar polls this endpoint and
// reloads only once the NEW version answers — /healthz alone is answered by
// the old process too, which is how "updated" tills kept running old code.
func TestUpdateStatus_ReportsRestartPending(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	old := updateStatusFn
	updateStatusFn = func() selfupdate.Status {
		return selfupdate.Status{Running: "0.22.1", Installed: "0.22.2", RestartPending: true}
	}
	t.Cleanup(func() { updateStatusFn = old })

	mux := http.NewServeMux()
	registerUpdateAPI(mux, &common.Deps{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/update/status: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Running        string `json:"running_version"`
			Installed      string `json:"installed_version"`
			RestartPending bool   `json:"restart_pending"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not the JSON envelope: %v (%s)", err, rec.Body.String())
	}
	if env.Error != nil || env.Data.Running != "0.22.1" || env.Data.Installed != "0.22.2" || !env.Data.RestartPending {
		t.Fatalf("envelope = %+v", env)
	}
}

// Same gate as apply: only someone allowed to update learns the versions.
func TestUpdateStatus_ManagerGate(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux := http.NewServeMux()
	registerUpdateAPI(mux, &common.Deps{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/update/status without manager: %d, want 403", rec.Code)
	}
}

// The success answer names the version being installed, so the status bar
// can wait for exactly that version to answer (ut-docs#2759 review).
func TestUpdateApplyInstalledResponseNamesVersion(t *testing.T) {
	rec := httptest.NewRecorder()
	respondUpdateApplyInstalled(rec, "0.22.2")
	var env struct {
		Data struct {
			Message string `json:"message"`
			Version string `json:"version"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("not the JSON envelope: %v (%s)", err, rec.Body.String())
	}
	if rec.Code != http.StatusOK || env.Error != nil || env.Data.Version != "0.22.2" || env.Data.Message == "" {
		t.Fatalf("apply success = %d %+v", rec.Code, env)
	}
}

// restart_pending=false alone is not proof: if the marker write failed, the
// OLD process reports false too and the page would reload onto the old
// build. The status bar must reload only when running_version is the
// version being installed.
func TestUpdateStatusPollReloadsOnlyOnTargetVersion(t *testing.T) {
	b, err := os.ReadFile("web/ui/layouts/base.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "fetch('/api/update/status'")
	if i < 0 {
		t.Fatal("base.html no longer polls /api/update/status after Update now")
	}
	poll := src[i:]
	if j := strings.Index(poll, "}, 2000);"); j > 0 {
		poll = poll[:j]
	}
	if !strings.Contains(poll, "st.running_version === target") {
		t.Error("the update poll does not compare running_version with the version being installed")
	}
	if !strings.Contains(src, "data.version") {
		t.Error("the status bar ignores the version the apply response names")
	}
}
