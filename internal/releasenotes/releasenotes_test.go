package releasenotes

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/web"
)

func note(version, date, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nversion: " + version + "\ndate: " + date + "\n---\n" + body)}
}

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"rn/en/v0.30.5.md":  note("v0.30.5", "2026-09-20", "## Fixed\n\n- Old fix.\n"),
		"rn/en/v0.30.6.md":  note("v0.30.6", "2026-09-28", "## New\n\n- A new thing.\n"),
		"rn/en/v0.4.0.md":   note("v0.4.0", "2026-01-02", "## Improved\n\n- Early.\n"),
		"rn/en/v0.30.10.md": note("v0.30.10", "2026-10-15", "## New\n\n- Ten.\n"),
		"rn/de/v0.30.6.md":  note("v0.30.6", "2026-09-28", "## Neu\n\n- Etwas Neues.\n"),
	}
}

func TestLoad_SortsBySemverNewestFirst(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	got := lib.versions
	want := []string{"v0.30.10", "v0.30.6", "v0.30.5", "v0.4.0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Versions() = %v, want %v (semver, not lexical)", got, want)
	}
}

func TestRecent_StartsAtRunningVersion(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	// The running build's note first, then older ones — never a newer one
	// the till isn't running yet.
	got := lib.Recent("en", "0.30.6", 5)
	if len(got) != 3 || got[0].Version != "v0.30.6" || got[1].Version != "v0.30.5" || got[2].Version != "v0.4.0" {
		t.Fatalf("Recent(0.30.6) = %v", versionsOf(got))
	}
	if got := lib.Recent("en", "v0.30.6", 2); len(got) != 2 {
		t.Fatalf("limit not applied: %v", versionsOf(got))
	}
}

func TestRecent_DevBuildShowsNewest(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	for _, running := range []string{"dev", "", "0.30.7-nightly.20260929.abc"} {
		got := lib.Recent("en", running, 2)
		if len(got) != 2 || got[0].Version != "v0.30.10" {
			t.Errorf("Recent(%q) = %v, want the newest notes", running, versionsOf(got))
		}
	}
}

func TestGet_FallsBackToEnglishPerNote(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	de := lib.Recent("de", "0.30.6", 2)
	if !de[0].Translated || de[0].Locale != "de" || !strings.Contains(string(de[0].HTML), "Etwas Neues") {
		t.Fatalf("v0.30.6 in de should be the German note: %+v", de[0])
	}
	if de[1].Translated || de[1].Locale != "en" || !strings.Contains(string(de[1].HTML), "Old fix") {
		t.Fatalf("v0.30.5 in de should fall back to English: %+v", de[1])
	}
	// Region tags resolve to their base language.
	if n, ok := lib.Get("de-DE", "v0.30.6"); !ok || n.Locale != "de" {
		t.Fatalf("de-DE should resolve to de: %+v", n)
	}
	if _, ok := lib.Get("en", "v9.9.9"); ok {
		t.Fatal("unknown version must not be found")
	}
}

func TestHas(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	if !lib.Has("0.30.6") || !lib.Has("v0.30.6") {
		t.Error("Has must accept the version with or without the v prefix")
	}
	if lib.Has("dev") || lib.Has("0.30.7") {
		t.Error("Has must be false for a version with no English note")
	}
}

func TestRender_ShiftsHeadingsAndDropsRawHTML(t *testing.T) {
	fsys := fstest.MapFS{
		"rn/en/v1.0.0.md": note("v1.0.0", "2026-01-01", "## New\n\n- Thing <script>alert(1)</script>\n"),
	}
	lib, err := Load(fsys, "rn")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := lib.Get("en", "v1.0.0")
	h := string(n.HTML)
	if !strings.Contains(h, "<h5") || strings.Contains(h, "<h2") {
		t.Errorf("## must render as <h5> inside the About card (h2 card, h3 What's new, h4 version): %s", h)
	}
	if strings.Contains(h, "<script>") {
		t.Errorf("raw HTML must not pass through: %s", h)
	}
}

func TestLoad_RejectsMalformedNotes(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"version mismatch": {"rn/en/v1.0.0.md": note("v1.0.1", "2026-01-01", "## New\n\n- x\n")},
		"bad filename":     {"rn/en/1.0.0.md": note("1.0.0", "2026-01-01", "## New\n\n- x\n")},
		"bad date":         {"rn/en/v1.0.0.md": note("v1.0.0", "28/09/2026", "## New\n\n- x\n")},
		"no front-matter":  {"rn/en/v1.0.0.md": &fstest.MapFile{Data: []byte("## New\n\n- x\n")}},
		"empty body":       {"rn/en/v1.0.0.md": note("v1.0.0", "2026-01-01", "\n")},
		"translation only": {"rn/de/v1.0.0.md": note("v1.0.0", "2026-01-01", "## Neu\n\n- x\n")},
	}
	for name, fsys := range cases {
		if _, err := Load(fsys, "rn"); err == nil {
			t.Errorf("%s: Load succeeded, want an error", name)
		}
	}
}

// The notes a shop owner reads must not carry PR numbers or card IDs
// (card ut-docs#3091 AC 2) — pinned over the real embedded files.
func TestBuiltin_NotesAreOwnerLanguage(t *testing.T) {
	lib, err := Load(web.ReleaseNotesFS, Root)
	if err != nil {
		t.Fatalf("embedded release notes do not load: %v", err)
	}
	if len(lib.versions) == 0 {
		t.Fatal("no embedded release notes")
	}
	ref := regexp.MustCompile(`#\d+|ut-docs|ADR-\d+|\bPR\b|github\.com`)
	for locale, notes := range lib.byLocale {
		for v, n := range notes {
			if m := ref.FindString(n.Markdown); m != "" {
				t.Errorf("%s/%s.md mentions %q — release notes are for shop owners, no PR/card numbers", locale, v, m)
			}
		}
	}
}

func TestNoticeVersion(t *testing.T) {
	t.Cleanup(func() { SetNoticeVersion("") })
	if NoticeVersion() != "" {
		t.Fatal("no notice by default")
	}
	SetNoticeVersion("v0.30.6")
	if NoticeVersion() != "v0.30.6" {
		t.Fatal("SetNoticeVersion not published")
	}
}

func TestTag(t *testing.T) {
	for in, want := range map[string]string{"0.30.6": "v0.30.6", "v0.30.6": "v0.30.6", " 1.2.3 ": "v1.2.3", "dev": "", "": "", "1.2": "", "0.30.7-nightly.1": ""} {
		if got := Tag(in); got != want {
			t.Errorf("Tag(%q) = %q, want %q", in, got, want)
		}
	}
}

func versionsOf(ns []*Note) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Version
	}
	return out
}

func TestRecent_VersionWithoutOwnNoteNeverShowsNewer(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	// A stamped build with no note of its own (a local -X Version= build):
	// start at the newest note older than it, never at v0.30.10.
	got := lib.Recent("en", "0.30.7", 5)
	if len(got) != 3 || got[0].Version != "v0.30.6" {
		t.Fatalf("Recent(0.30.7) = %v, want v0.30.6 first", versionsOf(got))
	}
	if got := lib.Recent("en", "0.1.0", 5); len(got) != 0 {
		t.Fatalf("Recent(0.1.0) = %v, want none (every note is newer)", versionsOf(got))
	}
}
