package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/plugins"
)

// installTipsSuggester installs a plugin that answers tips.suggest.ask with
// `answer`, holding payout:suggest only when grant is true.
func installTipsSuggester(t *testing.T, db *sql.DB, id string, grant bool, answer string, seen *string) {
	t.Helper()
	ctx := t.Context()
	m := &plugins.Manifest{ID: id, Name: id, Version: "1.0.0", Entrypoint: "./x",
		Hooks:       []plugins.ManifestHook{{Event: "tips.suggest.ask", Action: "tips"}},
		Permissions: []string{"events:receive", "payout:suggest"}}
	if err := plugins.PersistManifest(ctx, db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	perms := []string{"events:receive"}
	if grant {
		perms = append(perms, "payout:suggest")
	}
	for _, p := range perms {
		if err := plugins.GrantPermission(ctx, db, id, p); err != nil {
			t.Fatal(err)
		}
	}
	bus := plugins.SharedBus(db)
	bus.SetEventMode("tips.suggest.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(ctx, id, []string{"tips.suggest.ask"}, func(_ context.Context, ev plugins.Event) (json.RawMessage, error) {
		if seen != nil {
			*seen = string(ev.Payload)
		}
		return json.RawMessage(answer), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTipsSuggest_PrefillsRowsForRealUsersOnly(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO users(id,username,display_name,role,is_active) VALUES('worker1','worker1','Worker One','cashier',1)`); err != nil {
		t.Fatal(err)
	}
	var seen string
	installTipsSuggester(t, dp.Db, "tips-ok", true,
		`{"suggestions":[{"user_id":"worker1","amount_minor":1234,"currency":"GBP"},{"user_id":"ghost","amount_minor":999,"currency":"GBP"},{"user_id":"worker1","amount_minor":-5,"currency":"GBP"}],`+
			`"unmapped":[{"label":"Barber <b>Sam</b>","amount_minor":500,"currency":"GBP"}]}`, &seen)

	today := time.Now().Format("2006-01-02")
	rec := getWithUser(mux, "/api/reports/tips-suggestions?from="+today+"&to="+today, &auth.User{ID: "mgr1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Worker One") || !strings.Contains(body, `data-minor="1234"`) || !strings.Contains(body, "£12.34") {
		t.Fatalf("expected Worker One at £12.34 prefill, got: %s", body)
	}
	if strings.Contains(body, "ghost") || strings.Contains(body, "999") || strings.Contains(body, "-5") {
		t.Fatalf("unknown user id / negative amount must be dropped: %s", body)
	}
	if !strings.Contains(body, "Barber &lt;b&gt;Sam&lt;/b&gt;") || strings.Contains(body, "<b>Sam") {
		t.Fatalf("unmapped label must be listed and escaped: %s", body)
	}
	if !strings.Contains(seen, `"from":"`+today+`"`) || !strings.Contains(seen, `"to":"`+today+`"`) {
		t.Fatalf("plugin payload = %s, want the date range", seen)
	}
	if n := workerAllocationCount(t, dp); n != 0 {
		t.Fatalf("suggesting must write nothing, got %d worker_allocations", n)
	}
}

func TestTipsSuggest_GatedAndNeedsPermission(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp := newReportsPageTestDeps(t)
	installTipsSuggester(t, dp.Db, "tips-nogrant", false, `{"suggestions":[{"user_id":"mgr1","amount_minor":100,"currency":"GBP"}],"unmapped":[]}`, nil)
	today := time.Now().Format("2006-01-02")
	q := "/api/reports/tips-suggestions?from=" + today + "&to=" + today

	if rec := getWithUser(mux, q, &auth.User{ID: "u1", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: code %d, want 403", rec.Code)
	}
	rec := getWithUser(mux, q, &auth.User{ID: "mgr1", Role: "manager"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "data-minor") {
		t.Fatalf("a plugin without payout:suggest must give no rows: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"from=2026-13-99&to=" + today, "from=" + today, "from=" + today + "&to=2020-01-01"} {
		if rec := getWithUser(mux, "/api/reports/tips-suggestions?"+bad, &auth.User{ID: "mgr1", Role: "manager"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code %d, want 400", bad, rec.Code)
		}
	}
}
