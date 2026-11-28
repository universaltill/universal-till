package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db/plugindb"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

func postDataDelete(mux *http.ServeMux, id string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+id+"/data/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPluginDataDelete_NeedsManagerSession(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	mux := http.NewServeMux()
	registerPluginAPI(mux, &common.Deps{Cfg: &config.Config{}, Db: db})
	if rec := postDataDelete(mux, "some-plugin", url.Values{"confirm": {"yes"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("delete = %d, want 403", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/some-plugin/data/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestPluginDataDelete_ConfirmIDAndRetention(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	ctx := context.Background()
	id := "com.test.logbook"

	st, err := plugins.DataManager().Open(ctx, id, plugindb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(ctx, `CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.DataManager().UninstallRetained(id, 6, "plugins.data.delete_now.retained"); err != nil {
		t.Fatal(err)
	}

	if rec := postDataDelete(mux, id, url.Values{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no confirm = %d, want 400", rec.Code)
	}
	for _, bad := range []string{"..", "a%2Fb", "."} {
		if rec := postDataDelete(mux, bad, url.Values{"confirm": {"yes"}}); rec.Code == http.StatusOK {
			t.Fatalf("id %q accepted", bad)
		}
	}
	rec := postDataDelete(mux, id, url.Values{"confirm": {"yes"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("retained delete = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string         `json:"error"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" || body.Data["retain_until"] == nil {
		t.Fatalf("body %s err %v", rec.Body.String(), err)
	}
	if _, err := plugins.DataManager().RemovedInfo(id); err != nil {
		t.Fatalf("data must still be kept: %v", err)
	}

	// Status reports the retention.
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/"+id+"/data/status", nil)
	srec := httptest.NewRecorder()
	mux.ServeHTTP(srec, req)
	if srec.Code != http.StatusOK || !strings.Contains(srec.Body.String(), `"retained":true`) {
		t.Fatalf("status %d %s", srec.Code, srec.Body.String())
	}
}

func TestPluginDataDelete_NotRetainedDeletesAndAudits(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)
	ctx := context.Background()
	id := "com.test.plainrm"
	st, err := plugins.DataManager().Open(ctx, id, plugindb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(ctx, `CREATE TABLE t(v)`); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.DataManager().UninstallRetained(id, 0, ""); err != nil {
		t.Fatal(err)
	}
	if rec := postDataDelete(mux, id, url.Values{"confirm": {"yes"}}); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := plugins.DataManager().RemovedInfo(id); err == nil {
		t.Fatal("data should be gone")
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='plugin_data_delete_now'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows %d err %v", n, err)
	}
	if rec := postDataDelete(mux, id, url.Values{"confirm": {"yes"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}
}
