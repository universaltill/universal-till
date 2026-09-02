package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixture(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindViolationsInlineClosure(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func register(mux *http.ServeMux) {
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(violations), violations)
	}
	if violations[0].route != "/page" {
		t.Errorf("route = %q, want /page", violations[0].route)
	}
}

func TestFindViolationsFactoryHandler(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}
}

func register(mux *http.ServeMux) {
	mux.HandleFunc("/page", Handler())
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation (factory-call handler followed into its closure), got %d: %+v", len(violations), violations)
	}
}

func TestFindViolationsSkipsNonPageRoutes(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/page", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	})
	mux.HandleFunc("GET /ui/fragment", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	})
	mux.HandleFunc("POST /page", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	})
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("want 0 violations (api/ui/non-GET all out of scope), got %d: %+v", len(violations), violations)
	}
}

func TestFindViolationsAllowMarker(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func register(mux *http.ServeMux) {
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError) // page-error:allow reviewed exception
	})
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("want 0 violations (allow-marked line), got %d: %+v", len(violations), violations)
	}
}

// common.LocalizedError is ALSO a bare http.Error under the hood
// (translating the text doesn't add the rail back) — independent review
// finding on this guard's first version, which matched only the literal
// http.Error token.
func TestFindViolationsCatchesLocalizedError(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func register(mux *http.ServeMux) {
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
	})
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation (common.LocalizedError), got %d: %+v", len(violations), violations)
	}
}

func TestFindViolationsCatchesLogAndLocalizedError(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func register(mux *http.ServeMux) {
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "tag", err)
	})
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation (common.LogAndLocalizedError), got %d: %+v", len(violations), violations)
	}
}

// The exact shape tables_page.go's own reported incident had: a page-route
// closure calls a LOCAL closure declared earlier in the same enclosing
// function (a `requireManager`/`tiles`-style helper), and the bare error
// call lives inside THAT closure, not the route closure itself.
func TestFindViolationsFollowsLocalClosureIndirection(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func register(mux *http.ServeMux) {
	requireManager := func(w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		if !requireManager(w, r) {
			return
		}
	})
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation (through the local-closure indirection), got %d: %+v", len(violations), violations)
	}
}

// A local closure shared by a PAGE route and an API/fragment route must
// still be flagged — the violation is real regardless of what else calls
// the same helper (tables_page.go's own requireManager is exactly this:
// shared by /tables (a page) and several /api/tables/* POST routes).
func TestFindViolationsLocalClosureSharedWithApiRoute(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "fake", `package fake

import "net/http"

func register(mux *http.ServeMux) {
	requireManager := func(w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	mux.HandleFunc("GET /page", func(w http.ResponseWriter, r *http.Request) {
		requireManager(w, r)
	})
	mux.HandleFunc("POST /api/page", func(w http.ResponseWriter, r *http.Request) {
		requireManager(w, r)
	})
}
`)
	violations, err := findViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 violation (only the GET page route is in scope), got %d: %+v", len(violations), violations)
	}
}

// The real tree must stay clean — this is the guard's own regression test
// against a backslide, same intent as guard-page-http-error_test.sh's
// baseline check but exercised directly against the checker package.
func TestRealCodebaseIsClean(t *testing.T) {
	// scripts/ci/checkpagehttperror -> repo root is three levels up.
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	pagesDir := filepath.Join(root, "internal", "pages")
	if _, err := os.Stat(pagesDir); err != nil {
		t.Skipf("internal/pages not found at %s: %v", pagesDir, err)
	}
	violations, err := findViolations(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Errorf("real codebase has %d unmarked page-route http.Error violation(s): %+v", len(violations), violations)
	}
}
