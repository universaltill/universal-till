package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/restoredsecrets"
	"github.com/universaltill/universal-till/internal/secrets"
)

func getPlugins(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins = %d", rec.Code)
	}
	return rec.Body.String()
}

// U4 / S7: a restored till lists the plugin secrets to enter again (plugin and
// key only), and the owner can dismiss the banner.
func TestPluginsPage_RestoredSecretsBannerListsKeysNotValues(t *testing.T) {
	d := pluginsManagerTestDeps(t)
	dbPath := filepath.Join(t.TempDir(), "till.db")
	d.Cfg = &config.Config{DBPath: dbPath}
	seedTestPlugin(t, d.Db, "com.x.pay", "Pay", "1.0.0")
	secrets.SetDefault(nil) // the sealing key file did not come with the backup
	ciphertext := secrets.Prefix + "AAAAnotarealciphertextAAAA"
	if _, err := d.Db.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json, scope) VALUES ('s1','com.x.pay','api_key',?, 'global')`, ciphertext); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerPluginsPage(mux, d)

	if strings.Contains(getPlugins(t, mux), "restored-secrets-banner") {
		t.Fatal("no restore happened: no banner")
	}
	if err := restoredsecrets.MarkPending(dbPath); err != nil {
		t.Fatal(err)
	}
	body := getPlugins(t, mux)
	if !strings.Contains(body, `data-testid="restored-secrets-banner"`) ||
		!strings.Contains(body, "com.x.pay") || !strings.Contains(body, "api_key") {
		t.Fatalf("banner must list com.x.pay / api_key: %s", body)
	}
	if strings.Contains(body, "notarealciphertext") {
		t.Fatal("ciphertext leaked into the page")
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/plugins/restored-secrets/dismiss", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss = %d", rec.Code)
	}
	if strings.Contains(getPlugins(t, mux), "restored-secrets-banner") {
		t.Fatal("dismissed banner must stay hidden")
	}
}
