package pages

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#6554: an import can never forge a level the till sets itself
// (system = the Built in group) or any value outside the known levels.
func TestImportFromFile_RefusesUnknownTrustLevel_6554(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	deps := newPluginAPIDeps(t, db, nil)
	mux := http.NewServeMux()
	registerPluginAPI(mux, deps)

	manifest := `{"id":"com.test.forge","name":"F","version":"1.0.0","entrypoint":"./t.css","runtime":"wasm","canonical_type":"theme","device_arch":"any"}`
	for _, level := range []string{"system", "official", "Trusted ", "revoked"} {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("trust_level", level)
		fw, _ := mw.CreateFormFile("plugin_file", "p.tar.gz")
		_, _ = fw.Write(tarGzForTest(t, map[string]string{"manifest.json": manifest, "t.css": "x"}))
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/import-from-file", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid trust_level") {
			t.Errorf("trust_level %q: %d %q, want 400 invalid trust_level", level, rec.Code, rec.Body.String())
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.test.forge'`).Scan(&n)
		if n != 0 {
			t.Errorf("trust_level %q left a plugin row", level)
		}
	}
}
