package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// The export handler's own logic is argument validation; the archive/round-trip
// behaviour is covered by internal/plugins exporter tests. Assert the validation
// branch (which returns before touching the filesystem).
func TestHandleExportPlugin_RequiresVersion(t *testing.T) {
	h := handleExportPlugin(&common.Deps{})

	req := httptest.NewRequest(http.MethodGet, "/api/plugins/com.x/export", nil)
	req.SetPathValue("id", "com.x") // no ?version=
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing version, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// The uninstall handler removes files under ./data/plugins keyed by the id, so a
// traversal id must be rejected before any filesystem work.
func TestHandleUninstallPlugin_RejectsTraversalID(t *testing.T) {
	h := handleUninstallPlugin(&common.Deps{})
	for _, badID := range []string{"../etc", "a/b", `..\win`} {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/x/uninstall", nil)
		req.SetPathValue("id", badID)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q: expected 400, got %d", badID, rec.Code)
		}
	}
}
