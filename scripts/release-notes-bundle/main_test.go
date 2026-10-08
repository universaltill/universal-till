package main

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/releasenotes"
	"github.com/universaltill/universal-till/web"
)

func note(version, date, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nversion: " + version + "\ndate: " + date + "\n---\n" + body)}
}

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"rn/en/v0.30.6.md": note("v0.30.6", "2026-09-20", "## New\n\n- Something new.\n"),
		"rn/en/v0.30.7.md": note("v0.30.7", "2026-09-27", "## Fixed\n\n- A fix.\n"),
		"rn/de/v0.30.6.md": note("v0.30.6", "2026-09-20", "## Neu\n\n- Etwas Neues.\n"),
		"rn/de/README.txt": &fstest.MapFile{Data: []byte("ignored")},
		"rn/notes.txt":     &fstest.MapFile{Data: []byte("ignored")},
	}
}

func TestBundle_RoundTripsThroughLoadBundle(t *testing.T) {
	b, err := bundle(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Format int               `json:"format"`
		Files  map[string]string `json:"files"`
	}
	if err := json.Unmarshal(b, &shape); err != nil {
		t.Fatalf("bundle is not JSON: %v", err)
	}
	if shape.Format != releasenotes.BundleFormat || len(shape.Files) != 3 {
		t.Fatalf("bundle = format %d, %d files; want format %d, 3 .md files", shape.Format, len(shape.Files), releasenotes.BundleFormat)
	}
	if _, ok := shape.Files["de/v0.30.6.md"]; !ok {
		t.Fatalf("bundle keys must be <locale>/<file>.md relative to the root, got %v", shape.Files)
	}
	lib, err := releasenotes.LoadBundle(b)
	if err != nil {
		t.Fatalf("LoadBundle(bundle()) = %v", err)
	}
	n, ok := lib.Get("de", "v0.30.6")
	if !ok || !n.Translated || !strings.Contains(string(n.HTML), "Etwas Neues") {
		t.Fatalf("German note lost in the round trip: %+v", n)
	}
	if got := lib.Incoming("en", "0.30.5", "0.30.7"); len(got) != 2 || got[0].Version != "v0.30.7" {
		t.Fatalf("Incoming over the round-tripped bundle = %d notes, want v0.30.7 then v0.30.6", len(got))
	}
}

func TestBundle_RealNotesRoundTrip(t *testing.T) {
	b, err := bundle(web.ReleaseNotesFS, releasenotes.Root)
	if err != nil {
		t.Fatalf("the embedded notes must bundle and load back: %v", err)
	}
	if len(b) > 4<<20 {
		t.Fatalf("bundle is %d bytes, over the till's 4 MiB download cap", len(b))
	}
}

func TestBundle_RefusesMalformedSource(t *testing.T) {
	fsys := testFS()
	fsys["rn/en/v2.0.0.md"] = note("v2.0.1", "2026-01-01", "## New\n\n- x\n")
	if _, err := bundle(fsys, "rn"); err == nil {
		t.Fatal("bundle must refuse notes a till would refuse")
	}
	fsys = testFS()
	fsys["rn/fr/v0.30.6.md"] = note("v0.30.6", "2026-09-20", "## Nouveau\n\n- x\n")
	fsys["rn/fr/v9.9.9.md"] = note("v9.9.9", "2026-09-20", "## Nouveau\n\n- no English original\n")
	if _, err := bundle(fsys, "rn"); err == nil {
		t.Fatal("bundle must refuse a translation with no English original")
	}
}
