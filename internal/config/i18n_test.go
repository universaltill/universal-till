package config

import (
	"errors"
	"io/fs"
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

// errOpenFS fails Open unconditionally — used to force fs.ReadDir's own
// error branch, which no prior test (or NewI18nFS's happy path) ever hit.
type errOpenFS struct{ err error }

func (e errOpenFS) Open(string) (fs.File, error) { return nil, e.err }

func TestNewI18nFS_ReadDirError(t *testing.T) {
	_, err := NewI18nFS(errOpenFS{err: errors.New("boom")}, "en")
	if err == nil {
		t.Fatal("want an error when the FS can't be listed")
	}
}

// A malformed locale file must be reported, not silently skipped or
// partially loaded.
func TestNewI18nFS_MalformedJSON(t *testing.T) {
	fsys := fstest.MapFS{"en.json": &fstest.MapFile{Data: []byte(`{not valid json`)}}
	_, err := NewI18nFS(fsys, "en")
	if err == nil {
		t.Fatal("want an error for malformed locale JSON")
	}
}

// Non-JSON entries and subdirectories in the locales FS must be skipped,
// not treated as locale files.
func TestNewI18nFS_SkipsNonJSONAndDirs(t *testing.T) {
	fsys := fstest.MapFS{
		"en.json":      &fstest.MapFile{Data: []byte(`{"hello":"Hello"}`)},
		"README.md":    &fstest.MapFile{Data: []byte(`not a locale`)},
		"sub/note.txt": &fstest.MapFile{Data: []byte(`nested, not top-level`)},
	}
	i18n, err := NewI18nFS(fsys, "en")
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	if got := i18n.T("en", "hello"); got != "Hello" {
		t.Fatalf("T(en, hello) = %q", got)
	}
	if len(i18n.Available()) != 1 {
		t.Fatalf("Available() = %v, want only en", i18n.Available())
	}
}

// readFileErrFS lists one real entry via ReadDir but fails ReadFile for it —
// forces NewI18nFS's own read-after-list error branch, distinct from a
// directory-listing failure.
type readFileErrFS struct{ fs.FS }

func (r readFileErrFS) Open(name string) (fs.File, error) {
	if name == "en.json" {
		return nil, errors.New("simulated read failure")
	}
	return r.FS.Open(name)
}

func TestNewI18nFS_ReadFileError(t *testing.T) {
	base := fstest.MapFS{"en.json": &fstest.MapFile{Data: []byte(`{"hello":"Hello"}`)}}
	_, err := NewI18nFS(readFileErrFS{FS: base}, "en")
	if err == nil {
		t.Fatal("want an error when a listed locale file can't be read")
	}
}

// If the fallback locale has no file of its own, NewI18nFS must still
// succeed with an empty map for it (so T() never index-panics on a lookup
// against the fallback), not error out.
func TestNewI18nFS_MissingFallbackLocaleIsEmptyNotError(t *testing.T) {
	fsys := fstest.MapFS{"tr.json": &fstest.MapFile{Data: []byte(`{"hello":"Merhaba"}`)}}
	i18n, err := NewI18nFS(fsys, "en") // "en" has no file at all
	if err != nil {
		t.Fatalf("NewI18nFS: %v", err)
	}
	if got := i18n.T("en", "hello"); got != "hello" {
		t.Fatalf("T(en, hello) = %q, want the bare key back (untranslated)", got)
	}
}

// baseLang's region-stripping branch (idx > 0) is only exercised by a full
// BCP-47 tag; every existing fixture uses bare base-language codes already.
func TestT_FallsBackFromRegionTagToBaseLanguage(t *testing.T) {
	i := newTestI18n(t)
	if got := i.T("fa-IR", "basket.total"); got != "جمع" {
		t.Fatalf("T(fa-IR, basket.total) = %q, want the fa base translation", got)
	}
}
