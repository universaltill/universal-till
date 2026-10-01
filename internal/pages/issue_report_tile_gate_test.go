package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/uislot"
)

// ut-docs#3135: the /report-issue tile and the page must gate on the same
// action, or a role reaches the page without a tile (or sees a tile that 403s).
func TestReportIssueTile_GateMatchesPageAction(t *testing.T) {
	for _, e := range uislot.CoreMenu {
		if e.Href == "/report-issue" {
			if e.VisibleIf != issueReportingAction {
				t.Fatalf("/report-issue tile VisibleIf = %q, want %q (the page's action)", e.VisibleIf, issueReportingAction)
			}
			return
		}
	}
	t.Fatal("no /report-issue entry in uislot.CoreMenu")
}

func TestReportIssueTile_FollowsIssueReportingNotSettings(t *testing.T) {
	mux, dp := newMenuPageTestDeps(t, nil)
	dp.AuthSvc = auth.NewService(dp.Db)
	set := func(role, action string, granted int) {
		t.Helper()
		if _, err := dp.Db.Exec(`INSERT OR REPLACE INTO role_permissions(role, action, granted) VALUES(?,?,?)`, role, action, granted); err != nil {
			t.Fatalf("set %s/%s: %v", role, action, err)
		}
	}
	tile := func(role string) bool {
		t.Helper()
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/menu", nil), auth.User{ID: role + "-1", Role: role})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /menu = %d", rec.Code)
		}
		return strings.Contains(rec.Body.String(), `href="/report-issue"`)
	}

	// issue_reporting without settings: tile shown.
	set("cashier", "issue_reporting", 1)
	set("cashier", "settings", 0)
	if !tile("cashier") {
		t.Error("role with issue_reporting but not settings must see the /report-issue tile")
	}
	// settings without issue_reporting: tile hidden.
	set("cashier", "issue_reporting", 0)
	set("cashier", "settings", 1)
	if tile("cashier") {
		t.Error("role with settings but not issue_reporting must not see the /report-issue tile")
	}
}

// ut-docs#3135 review: the /settings "Report an issue" card and its
// sidebar row lead to /report-issue too, so they follow issue_reporting,
// not the settings action the page itself is gated on.
func TestSettingsPage_ReportIssueCardFollowsIssueReporting(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	get := func() string {
		t.Helper()
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("manager GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	if body := get(); !strings.Contains(body, `id="settings-issuereport"`) || !strings.Contains(body, `data-key="settings-issuereport"`) {
		t.Fatal("manager with issue_reporting must see the Report an issue card and its sidebar row")
	}
	if _, err := d.Db.Exec(`UPDATE role_permissions SET granted = 0 WHERE role = 'manager' AND action = 'issue_reporting'`); err != nil {
		t.Fatal(err)
	}
	body := get()
	if strings.Contains(body, `id="settings-issuereport"`) {
		t.Error("manager without issue_reporting must not see the Report an issue card (its button would 403)")
	}
	if strings.Contains(body, `data-key="settings-issuereport"`) {
		t.Error("manager without issue_reporting must not see the Report an issue sidebar row")
	}
}
