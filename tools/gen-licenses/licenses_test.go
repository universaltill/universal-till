package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureGoMod = `module example.test/app

go 1.27.1

require (
	github.com/a/alpha v1.2.3
	// a comment
	github.com/b/beta v0.0.0-20200101000000-abcdef123456
	github.com/universaltill/universal-till/catimport v0.0.0
)

require github.com/c/gamma v2.0.0+incompatible // indirect

require (
	github.com/d/delta v1.0.0 // indirect
)

replace github.com/b/beta => ./local/beta
`

func TestParseRequires(t *testing.T) {
	got := ParseRequires(fixtureGoMod)
	var paths []string
	for _, m := range got {
		paths = append(paths, m.Path+"@"+m.Version)
	}
	want := "github.com/a/alpha@v1.2.3 github.com/b/beta@v0.0.0-20200101000000-abcdef123456 github.com/c/gamma@v2.0.0+incompatible github.com/d/delta@v1.0.0"
	if strings.Join(paths, " ") != want {
		t.Errorf("got %v\nwant %s", paths, want)
	}
}

func TestIsLicenceFileName(t *testing.T) {
	for n, want := range map[string]bool{
		"LICENSE": true, "LICENSE.md": true, "LICENSE.txt": true, "License": true, "COPYING": true, "NOTICE": true,
		"LICENCE-MIT": true, "UNLICENSE": true, "PATENTS": true,
		"license_test.go": false, "licenses.go": false, "README.md": false, "main.go": false,
	} {
		if IsLicenceFileName(n) != want {
			t.Errorf("IsLicenceFileName(%q) = %v, want %v", n, !want, want)
		}
	}
}

func fakeRead(m Module) ([]LicenceFile, error) {
	return []LicenceFile{{Name: "LICENSE", Text: "Licence text of " + m.Path + "\r\n"}}, nil
}

func TestRenderListsEveryModule(t *testing.T) {
	mods := ParseRequires(fixtureGoMod)
	text, err := Render(mods, fakeRead)
	if err != nil {
		t.Fatal(err)
	}
	if missing := MissingFrom(mods, text); len(missing) != 0 {
		t.Errorf("rendered file lacks %v", missing)
	}
	if !strings.Contains(text, "Licence text of github.com/d/delta") || strings.Contains(text, "\r") {
		t.Error("licence text missing or CRLF kept")
	}
}

// Seeded failure: a module added to go.mod after the file was generated is
// reported, and a version bump counts as stale too.
func TestMissingFrom_SeededStaleness(t *testing.T) {
	mods := ParseRequires(fixtureGoMod)
	text, _ := Render(mods, fakeRead)
	added := append([]Module{{Path: "github.com/e/epsilon", Version: "v1.0.0"}}, mods...)
	if m := MissingFrom(added, text); len(m) != 1 || m[0] != "github.com/e/epsilon@v1.0.0" {
		t.Errorf("seeded new module not reported: %v", m)
	}
	bumped := append([]Module(nil), mods...)
	bumped[0].Version = "v9.9.9"
	if m := MissingFrom(bumped, text); len(m) != 1 {
		t.Errorf("version bump not reported: %v", m)
	}
}

func TestRender_ModuleWithoutLicenceFails(t *testing.T) {
	_, err := Render([]Module{{Path: "github.com/x/none", Version: "v1.0.0"}}, func(Module) ([]LicenceFile, error) { return nil, nil })
	if err == nil || !strings.Contains(err.Error(), "github.com/x/none") {
		t.Errorf("want an error naming the module, got %v", err)
	}
}

func TestReadFromDir(t *testing.T) {
	dir := t.TempDir()
	for n, c := range map[string]string{"LICENSE": "MIT text", "NOTICE": "notice", "main.go": "package x", "license_test.go": "x"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readFromDir(dir)
	if err != nil || len(files) != 2 || files[0].Name != "LICENSE" || files[1].Name != "NOTICE" {
		t.Errorf("files = %+v, err = %v", files, err)
	}
}

// The committed page lists every module in the real go.mod, at its version.
// Run from tools/gen-licenses, so the repo root is two levels up.
func TestCommittedLicencesListEveryGoModModule(t *testing.T) {
	gomod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile("../../" + outFile)
	if err != nil {
		t.Fatalf("%s missing: run `make licenses`: %v", outFile, err)
	}
	mods := ParseRequires(string(gomod))
	if len(mods) < 10 {
		t.Fatalf("parsed only %d modules from go.mod: parser broken", len(mods))
	}
	if missing := MissingFrom(mods, string(page)); len(missing) != 0 {
		t.Errorf("%s is stale, lacks %v: run `make licenses`", outFile, missing)
	}
}

// Review M4: the shipped front-end code and the Go runtime are listed too.
// A vendored file with no licence entry fails; so does a stale entry.
func TestUncoveredVendored_Seeded(t *testing.T) {
	comps := []Component{{Name: "a", Covers: []string{"a.js", "gone.js"}}}
	unc, stale := UncoveredVendored([]string{"a.js", "new/lib.js"}, comps)
	if len(unc) != 1 || unc[0] != "new/lib.js" {
		t.Errorf("uncovered = %v, want [new/lib.js]", unc)
	}
	if len(stale) != 1 || stale[0] != "gone.js" {
		t.Errorf("stale = %v, want [gone.js]", stale)
	}
}

func TestVendoredFilesSkipsLicenceAndReadme(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"x.js", "sub/y.wasm", "sub/README.md", "sub/LICENSE.y"} {
		p := filepath.Join(dir, n)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := vendoredFiles(dir)
	if err != nil || strings.Join(got, ",") != "sub/y.wasm,x.js" {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestRealVendorDirIsCoveredAndLicencesExist(t *testing.T) {
	files, err := vendoredFiles("../../" + vendorDir)
	if err != nil || len(files) < 5 {
		t.Fatalf("vendored files: %v, %v", files, err)
	}
	if unc, stale := UncoveredVendored(files, OtherComponents); len(unc) > 0 || len(stale) > 0 {
		t.Errorf("uncovered %v stale %v", unc, stale)
	}
	text, err := RenderComponents(OtherComponents, func(p string) (string, error) {
		b, err := os.ReadFile("../../" + p)
		return string(b), err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Go runtime and standard library", "htmx | 1.9.12", "Alpine.js | 3.14.1", "ONNX Runtime Web", "Lucide icons"} {
		if !strings.Contains(text, want) {
			t.Errorf("components section lacks %q", want)
		}
	}
}

func TestRenderComponents_MissingLicenceFileFails(t *testing.T) {
	_, err := RenderComponents([]Component{{Name: "z", LicenceFiles: []string{"nope"}}}, func(string) (string, error) { return "", os.ErrNotExist })
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("want error naming the file, got %v", err)
	}
}
