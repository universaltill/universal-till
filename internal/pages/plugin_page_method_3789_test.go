package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#3789: plugin pages are read-only, so only GET/HEAD may render them.
func TestPluginPage_OnlyGetAndHeadRender_3789(t *testing.T) {
	d, _ := pluginPageTestDeps(t)
	seedTestPlugin(t, d.Db, "com.x.faq", "FAQ Plugin", "1.2.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.faq','page','faq-root','/faq','Root FAQ'),('e2','com.x.faq','page','faq-sub','/plugin/faq','Sub FAQ')`); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerIndex(mux, d)
	registerPluginPages(mux, d)

	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}

	for _, tc := range []struct{ path, label string }{
		{"/faq", "Root FAQ"},
		{"/plugin/faq", "Sub FAQ"},
	} {
		if rec := do(http.MethodGet, tc.path); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.label) {
			t.Errorf("GET %s = %d, want 200 rendering %q", tc.path, rec.Code, tc.label)
		}
		if rec := do(http.MethodHead, tc.path); rec.Code != http.StatusOK {
			t.Errorf("HEAD %s = %d, want 200", tc.path, rec.Code)
		}
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			rec := do(m, tc.path)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", m, tc.path, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
				t.Errorf("%s %s Allow = %q, want %q", m, tc.path, got, "GET, HEAD")
			}
		}
	}

	for _, p := range []string{"/nope", "/plugin/nope"} {
		if rec := do(http.MethodPost, p); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s (no entry) = %d, want 404", p, rec.Code)
		}
	}
}
