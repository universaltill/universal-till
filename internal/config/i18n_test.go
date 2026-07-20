package config

import (
	"os"
	"testing"
	"testing/fstest"
)

// TestNewI18nFS_WorksFromAnyWorkingDirectory guards the real startup crash
// found while walking through a from-scratch install: NewI18n used to
// os.ReadDir a plain "web/locales" path relative to the CWD, and the
// caller (internal/pages/init.go) wrapped a failure in log.Fatalf — so a
// packaged install launched from a working directory without that
// subdirectory crashed on startup, every time, before serving a single
// request. NewI18nFS takes an fs.FS instead (production passes the
// embedded web/locales.FS); this must work with zero filesystem context.
func TestNewI18nFS_WorksFromAnyWorkingDirectory(t *testing.T) {
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

	fsys := fstest.MapFS{
		"en.json": &fstest.MapFile{Data: []byte(`{"hello":"Hello"}`)},
		"tr.json": &fstest.MapFile{Data: []byte(`{"hello":"Merhaba"}`)},
	}

	i18n, err := NewI18nFS(fsys, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	if got := i18n.T("en", "hello"); got != "Hello" {
		t.Fatalf("T(en, hello) = %q, want Hello", got)
	}
	if got := i18n.T("tr", "hello"); got != "Merhaba" {
		t.Fatalf("T(tr, hello) = %q, want Merhaba", got)
	}
}

// TestNewI18n_BackCompatWithOSDirectory confirms the original
// directory-path constructor still works (existing tests/callers pass a
// real web/locales checkout) — NewI18nFS is additive, not a breaking change.
func TestNewI18n_BackCompatWithOSDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/en.json", []byte(`{"hello":"Hello"}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	i18n, err := NewI18n(dir, "en")
	if err != nil {
		t.Fatalf("NewI18n: %v", err)
	}
	if got := i18n.T("en", "hello"); got != "Hello" {
		t.Fatalf("T(en, hello) = %q, want Hello", got)
	}
}
