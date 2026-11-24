package pages

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#6958: a release that exists but is held back for a test build must
// not read as "up to date", and must never be offered or installed.

func TestUpdateCheck_HeldBackSaysSo(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newAutoUpdateTestDeps(t)
	stubAutoUpdateSeams(t, updates.Status{}, updates.Status{}, true, nil)
	orig := updateCheckNow
	t.Cleanup(func() { updateCheckNow = orig })
	updateCheckNow = func(context.Context) updates.Status {
		return updates.Status{Latest: "0.36.3", HeldBack: true}
	}
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update/check", nil))
	body := rec.Body.String()
	if !strings.Contains(body, html.EscapeString(httpx.T("en", "settings.update.held_back"))) {
		t.Fatalf("held-back line missing: %q", body)
	}
	if strings.Contains(body, httpx.T("en", "settings.update.up_to_date")) || strings.Contains(body, "/api/update/apply") {
		t.Fatalf("held back must neither say up to date nor offer Update now: %q", body)
	}
}

func TestSetupUpdateCheck_HeldBackSaysSo(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	stubSetupUpdateSeams(t, updates.Status{Latest: "0.36.3", HeldBack: true}, true, nil)
	rec := postSetupUpdate(t, mux, "/api/setup/update-check")
	body := rec.Body.String()
	if !strings.Contains(body, html.EscapeString(httpx.T("en", "settings.update.held_back"))) {
		t.Fatalf("wizard held-back line missing: %q", body)
	}
	if strings.Contains(body, httpx.T("en", "settings.update.up_to_date")) {
		t.Fatalf("wizard must not say up to date: %q", body)
	}
}

func TestUpdateApply_HeldBackIs409NewerData(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newAutoUpdateTestDeps(t)
	orig := updateApplyCheckNow
	t.Cleanup(func() { updateApplyCheckNow = orig })
	updateApplyCheckNow = func(context.Context) updates.Status {
		return updates.Status{Latest: "0.36.3", HeldBack: true}
	}
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/update/apply", nil))
	if rec.Code == http.StatusBadRequest {
		t.Skip("in-app update unsupported on this test binary; covered by the key/status unit test")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body %q, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), httpx.T("en", "settings.update.error.newer_data")) {
		t.Fatalf("body lacks newer_data text: %q", rec.Body.String())
	}
}

func TestAutoUpdateTick_HeldBackNeverApplies(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true, Latest: "0.36.3"}, updates.Status{Latest: "0.36.3", HeldBack: true}, true, nil)
	autoUpdateTick(t.Context(), dp, tickNow)
	if *applyCalls != 0 {
		t.Fatalf("auto-update applied a held-back release (%d calls)", *applyCalls)
	}
}
