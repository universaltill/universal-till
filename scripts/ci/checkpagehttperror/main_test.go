package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The scanner reads real Go source, so the unit fixtures are real (temp) Go
// files — same style as checkhelptopics/main_test.go's TestPageRoutesFinds
// Registrations.

func writeFixture(t *testing.T, dir, name, src string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A bare http.Error(...) reachable directly inside a GET page-route
// handler's literal must be flagged — this is the exact bug class
// ut-docs#1455 fixed and this guard exists to prevent a regression of.
func TestCheckFile_FlagsDirectHTTPErrorInPageHandler(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "fixture.go", `package pages

import "net/http"

func registerFixture(mux *http.ServeMux) {
	mux.HandleFunc("GET /fixture", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
}
`)
	violations, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want exactly 1", violations)
	}
	if violations[0].call != "http.Error" || violations[0].pattern != "/fixture" {
		t.Errorf("violation = %+v, want call=http.Error pattern=/fixture", violations[0])
	}
}

// common.LocalizedError / common.LogAndLocalizedError are http.Error under
// the hood (internal/pages/common/errors.go) and must be flagged the same
// way.
func TestCheckFile_FlagsCommonWrappers(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "fixture.go", `package pages

import "net/http"

func registerFixture(mux *http.ServeMux) {
	mux.HandleFunc("/bare-page", func(w http.ResponseWriter, r *http.Request) {
		common.LocalizedError(w, r, http.StatusForbidden, "some.key")
	})
	mux.HandleFunc("GET /other-page", func(w http.ResponseWriter, r *http.Request) {
		common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "some.key", "tag", err)
	})
}
`)
	violations, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(violations) != 2 {
		t.Fatalf("violations = %v, want exactly 2", violations)
	}
}

// A one-level-indirected local closure (e.g. tables_page.go's `tiles :=
// func(...) {...}`, called by name from the page-route handler) must also
// be scanned — this is the real shape the /tables bug shipped as.
func TestCheckFile_FollowsOneLevelOfLocalClosureIndirection(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "fixture.go", `package pages

import "net/http"

func registerFixture(mux *http.ServeMux) {
	helper := func(w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, "boom", http.StatusInternalServerError)
		return false
	}
	mux.HandleFunc("GET /fixture", func(w http.ResponseWriter, r *http.Request) {
		if !helper(w, r) {
			return
		}
	})
}
`)
	violations, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("violations = %v, want exactly 1 (found via one-level closure indirection)", violations)
	}
}

// The "// page-error:allow <reason>" escape hatch — same convention as
// i18n:ignore / kiosk-engine-guard:allow — must suppress a flagged line.
func TestCheckFile_PageErrorAllowSuppressesViolation(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "fixture.go", `package pages

import "net/http"

func registerFixture(mux *http.ServeMux) {
	mux.HandleFunc("GET /fixture", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError) // page-error:allow zz-guard-test fixture, reviewed
	})
}
`)
	violations, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (allowlisted)", violations)
	}
}

// A non-page route (POST-only, or under a denylisted prefix like /api/)
// must never be flagged — this guard is scoped to top-level page GET
// routes only.
func TestCheckFile_IgnoresNonPageRoutes(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "fixture.go", `package pages

import "net/http"

func registerFixture(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/fixture", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /api/fixture", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
}
`)
	violations, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (POST-only and /api/ are out of scope)", violations)
	}
}

// A handler registered via a call to a separately-defined function (not an
// inline literal, e.g. plugins_store_page.go's
// mux.HandleFunc("/plugins/store", PluginStoreHandler(deps))) is deeper
// indirection than the documented one-level scope — must not panic, and
// must simply not be scanned (a known, documented boundary, not a bug).
func TestCheckFile_HandlerByFunctionCallIsOutOfScope(t *testing.T) {
	dir := t.TempDir()
	path := writeFixture(t, dir, "fixture.go", `package pages

import "net/http"

func registerFixture(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("/fixture", SomeHandler(d))
}
`)
	violations, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (handler is a func call, out of the one-level scope)", violations)
	}
}

// Integration: the real internal/pages tree, after ut-docs#1455's own
// migration + page-error:allow annotations on the pre-existing (ut-docs#1458
// scope) call sites, must be fully clean — a coverage regression fails
// `go test ./...` too, not only the guard step.
func TestRealPagesTreeHasNoUnallowedViolations(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	pagesDir := filepath.Join(root, "internal", "pages")
	violations, err := scanDir(pagesDir)
	if err != nil {
		t.Fatalf("scanDir(%s): %v", pagesDir, err)
	}
	if len(violations) > 0 {
		t.Errorf("unallowed page-route http.Error violations in the real tree: %v", violations)
	}
}
