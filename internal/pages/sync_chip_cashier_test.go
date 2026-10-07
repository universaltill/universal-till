package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3079: the rail's sync chip linked every operator to /tills (or
// /sync-quarantine), both sync_management-gated — so a cashier tapped into
// a refusal. For a viewer without sync_management the chip keeps its look,
// class and accessible text but is not a link; a manager keeps the link.
func TestSyncChip_CashierGetsStatusNotLink(t *testing.T) {
	for _, mode := range []string{"replica", "primary"} {
		t.Run(mode, func(t *testing.T) {
			dp := newMigratedSyncDeps(t, mode+"-chip-3079.db")
			initPagesI18n(t)
			dp.AuthSvc = auth.NewService(dp.Db)
			t.Setenv("UT_AUTH", "on")
			ctx := t.Context()
			if mode == "replica" {
				if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
					t.Fatal(err)
				}
				if err := dp.Settings.Set(ctx, "sync.till_name", "Front Till"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := dp.Settings.Set(ctx, "till.name", "Front Till"); err != nil {
					t.Fatal(err)
				}
				if _, err := data.NewTillsRepo(dp.Db).InsertTill(ctx, "Replica 1", hashBearer("token-3079"), data.TillRoleAdditional); err != nil {
					t.Fatal(err)
				}
			}
			mux := http.NewServeMux()
			registerSyncAdmin(mux, dp)
			get := func(role string) string {
				req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/sync-chip", nil), auth.User{ID: "u-" + role, Role: role})
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s GET /ui/sync-chip = %d", role, rec.Code)
				}
				return rec.Body.String()
			}
			cashier := get("cashier")
			if strings.Contains(cashier, "href=") {
				t.Fatalf("cashier sync chip must not link anywhere, got %q", cashier)
			}
			for _, want := range []string{`sync-chip warn`, `class="nav-toggle"`, "Front Till", `aria-label="`} {
				if !strings.Contains(cashier, want) {
					t.Fatalf("cashier sync chip lost %q (look/state must stay), got %q", want, cashier)
				}
			}
			manager := get("manager")
			if !strings.Contains(manager, `<a href="/tills"`) {
				t.Fatalf("manager sync chip must still link /tills, got %q", manager)
			}
		})
	}
}
