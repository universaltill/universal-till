package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3438 (personal display, part 2/3): every page render and the
// theme-sync poll read the theme through Deps.ResolvedTheme(r), and the sell
// screen reads its browsing mode through Deps.ResolvedBrowsingMode(r). The
// personal_display grant is not seeded to any role (part 3, #3440), so these
// tests insert it by hand.

const (
	pd3438Till     = "slate"
	pd3438Personal = "amber"
)

type pd3438Rig struct {
	mux *http.ServeMux
	d   *common.Deps
}

func newPD3438Rig(t *testing.T) pd3438Rig {
	t.Helper()
	t.Setenv("UT_AUTH", "on")
	initPagesI18n(t)
	d := newDesignerTestDeps(t)
	st := d.CurrentState()
	st.Theme = pd3438Till
	d.SetState(st)
	for _, id := range []string{"u-mgr", "u-mgr2"} {
		if _, err := d.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES (?,?,?,'x','manager')`, id, id, id); err != nil {
			t.Fatalf("seed user %s: %v", id, err)
		}
	}
	// An empty catalogue renders the "no products" state, not the browsing
	// layout, so the sell screen needs one item.
	if _, err := d.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itm1','ABC','Apple',100,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	mux := http.NewServeMux()
	registerMenu(mux, d)
	registerAdmin(mux, d)
	registerSettings(mux, d)
	registerHelp(mux, d)
	registerThemeSync(mux, d)
	registerButtonsAPI(mux, d)
	registerReportsPage(mux, d)
	registerInventoryPage(mux, d)
	return pd3438Rig{mux: mux, d: d}
}

func (r pd3438Rig) grant(t *testing.T) {
	t.Helper()
	if _, err := r.d.Db.Exec(`INSERT INTO role_permissions(role,action,granted) VALUES('manager','personal_display',1)`); err != nil {
		t.Fatalf("grant personal_display: %v", err)
	}
}

func (r pd3438Rig) set(t *testing.T, user, key, val string) {
	t.Helper()
	if err := data.NewUserDisplayRepo(r.d.Db).Set(context.Background(), user, key, val); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func (r pd3438Rig) get(t *testing.T, path, user string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = auth.WithUser(req, auth.User{ID: user, Role: "manager"})
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %.300s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// pd3438Pages are the migrated routes; /ui/theme-sync is the poll.
var pd3438Pages = []string{"/admin", "/settings", "/help", "/reports", "/inventory", "/ui/theme-sync"}

func TestPersonalDisplay3438_NoOverridesByteIdentical(t *testing.T) {
	r := newPD3438Rig(t)
	// The first /settings load writes default setting rows, so it is not
	// byte-comparable with later loads; warm it up once.
	for _, p := range pd3438Pages {
		r.get(t, p, "u-mgr")
	}
	shipped := map[string]string{}
	for _, p := range pd3438Pages {
		shipped[p] = r.get(t, p, "u-mgr")
		if !strings.Contains(shipped[p], `data-theme="`+pd3438Till+`"`) {
			t.Fatalf("%s lacks the till theme: %.400s", p, shipped[p])
		}
	}
	r.grant(t) // granted, but no personal row yet
	for _, p := range pd3438Pages {
		if got := r.get(t, p, "u-mgr"); got != shipped[p] {
			t.Errorf("%s differs once granted with no row", p)
		}
	}
	if !strings.Contains(shipped["/settings"], `<option value="`+pd3438Till+`" selected>`) {
		t.Errorf("settings picker must preselect the till theme")
	}
}

func TestPersonalDisplay3438_ManagerSeesPersonalTheme(t *testing.T) {
	r := newPD3438Rig(t)
	r.grant(t)
	r.set(t, "u-mgr", common.KeyTheme, pd3438Personal)
	for _, p := range pd3438Pages {
		if body := r.get(t, p, "u-mgr"); !strings.Contains(body, `data-theme="`+pd3438Personal+`"`) {
			t.Errorf("%s: manager with a personal theme must see %q: %.300s", p, pd3438Personal, body)
		}
		// Another operator on the same till keeps the till theme.
		if body := r.get(t, p, "u-mgr2"); !strings.Contains(body, `data-theme="`+pd3438Till+`"`) {
			t.Errorf("%s: other operator must keep %q", p, pd3438Till)
		}
	}
}

func TestPersonalDisplay3438_SettingsPickerKeepsTillTheme(t *testing.T) {
	r := newPD3438Rig(t)
	r.grant(t)
	r.set(t, "u-mgr", common.KeyTheme, pd3438Personal)
	body := r.get(t, "/settings", "u-mgr")
	if !strings.Contains(body, `href="/themes/`+pd3438Personal+`.css`) {
		t.Errorf("settings page must wear the personal theme")
	}
	if !strings.Contains(body, `<option value="`+pd3438Till+`" selected>`) {
		t.Errorf("settings picker must still preselect the till theme")
	}
	if strings.Contains(body, `<option value="`+pd3438Personal+`" selected>`) {
		t.Errorf("settings picker must not preselect the personal theme")
	}
}

func TestPersonalDisplay3438_SellScreenPersonalBrowsingMode(t *testing.T) {
	r := newPD3438Rig(t)
	r.grant(t)
	st := r.d.CurrentState()
	st.BrowsingMode = common.DefaultBrowsingMode
	r.d.SetState(st)
	if body := r.get(t, "/ui/buttons", "u-mgr"); !strings.Contains(body, `id="browsing-category-tiles"`) {
		t.Fatalf("till mode should be category tiles: %.300s", body)
	}
	r.set(t, "u-mgr", common.KeyBrowsingMode, common.BrowsingModeStripOverflow)
	if body := r.get(t, "/ui/buttons", "u-mgr"); strings.Contains(body, `id="browsing-category-tiles"`) || !strings.Contains(body, `class="products-strip"`) {
		t.Errorf("personal strip mode not rendered: %.300s", body)
	}
	if body := r.get(t, "/ui/buttons", "u-mgr2"); !strings.Contains(body, `id="browsing-category-tiles"`) {
		t.Errorf("other operator must keep the till mode")
	}
	r.set(t, "u-mgr", common.KeyBrowsingMode, "bogus")
	if body := r.get(t, "/ui/buttons", "u-mgr"); !strings.Contains(body, `id="browsing-category-tiles"`) {
		t.Errorf("invalid personal mode must clamp to category tiles: %.300s", body)
	}
}

func TestPersonalDisplay3438_ExclusionsKeepTillTheme(t *testing.T) {
	r := newPD3438Rig(t)
	r.grant(t)
	r.set(t, "u-mgr", common.KeyTheme, pd3438Personal)
	httpx.InitTheme(pd3438Till)
	t.Cleanup(func() { httpx.InitTheme("") })
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = auth.WithUser(req, auth.User{ID: "u-mgr", Role: "manager"})
	rec := httptest.NewRecorder()
	httpx.RenderError(rec, req, http.StatusInternalServerError, "error.generic", nil)
	if body := rec.Body.String(); !strings.Contains(body, `data-theme="`+pd3438Till+`"`) || strings.Contains(body, pd3438Personal) {
		t.Errorf("error page must keep the till theme: %.400s", body)
	}
}

var themeFromStateField = regexp.MustCompile(`"theme":\s*\w+\.Theme\b`)

// TestNoRenderSiteReadsTillThemeDirectly stops a new page from reading the
// till theme straight from RuntimeState instead of the resolver.
func TestNoRenderSiteReadsTillThemeDirectly(t *testing.T) {
	chdirRoot(t)
	root := filepath.Join("internal", "pages")
	allowed := map[string]bool{
		"internal/pages/cloudsync_wire.go":          true, // check-in payload, no viewer
		"internal/pages/common/personal_display.go": true, // the resolver's fall-through
	}
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), "CurrentState().Theme") && !allowed[filepath.ToSlash(p)] {
			t.Errorf("%s reads CurrentState().Theme; use d.ResolvedTheme(r)", p)
		}
		// Indirect reads (st := d.CurrentState(); "theme": st.Theme) render the
		// till theme too; the till picker's value is "tillTheme", not "theme".
		if loc := themeFromStateField.FindIndex(b); loc != nil {
			t.Errorf("%s renders \"theme\" from a state field (%s); use d.ResolvedTheme(r)", p, b[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
