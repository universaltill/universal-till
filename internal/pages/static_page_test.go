package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestRegisterStatic_ServesEmbeddedAssetsFromAnyWorkingDirectory guards the
// same class of bug as httpx's Render/RenderPartial tests: the static file
// server used to be http.FileServer(http.Dir("web/public")) — a plain disk
// read relative to the CWD, so a packaged install launched from anywhere
// else served zero CSS/JS/logo/theme assets (a silently broken, unstyled,
// non-interactive UI, not just a missing page). It's embedded now.
func TestRegisterStatic_ServesEmbeddedAssetsFromAnyWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	mux := http.NewServeMux()
	registerStatic(mux)

	for _, path := range []string{"/public/app.css", "/public/vendor/alpine.min.js"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200 from an unrelated CWD, got %d", path, w.Code)
		}
		if w.Body.Len() == 0 {
			t.Fatalf("%s: expected a non-empty body", path)
		}
	}
}

// TestRegisterStatic_DiskOverridesTakePriority confirms a shop's on-disk
// customization (an uploaded item image, a swapped logo/theme) still wins
// over the embedded default when web/public actually exists — the fix
// shouldn't make disk-based overrides stop working.
func TestRegisterStatic_DiskOverridesTakePriority(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	if err := os.MkdirAll("web/public", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile("web/public/app.css", []byte("/* shop override */"), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}

	mux := http.NewServeMux()
	registerStatic(mux)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/public/app.css", nil)
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Body.String(); got != "/* shop override */" {
		t.Fatalf("expected the on-disk override to win over the embedded default, got %q", got)
	}
}
