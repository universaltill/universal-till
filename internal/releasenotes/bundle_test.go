package releasenotes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ut-docs#3940: a release ships its notes as release-notes.json so a till
// can show the INCOMING version's notes before it installs it.

func rawNote(version, date, body string) string {
	return "---\nversion: " + version + "\ndate: " + date + "\n---\n" + body
}

func bundleJSON(t *testing.T, files map[string]string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"format": 1, "files": files})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoadBundle_RejectsMalformedInput(t *testing.T) {
	good := rawNote("v1.0.0", "2026-01-01", "## New\n\n- x\n")
	cases := map[string][]byte{
		"not json":           []byte("<html>"),
		"wrong format":       []byte(`{"format":2,"files":{"en/v1.0.0.md":` + jsonString(good) + `}}`),
		"no files":           []byte(`{"format":1,"files":{}}`),
		"bad filename":       bundleJSON(t, map[string]string{"en/1.0.0.md": good}),
		"version mismatch":   bundleJSON(t, map[string]string{"en/v1.0.1.md": good}),
		"translation only":   bundleJSON(t, map[string]string{"de/v1.0.0.md": good}),
		"path traversal":     bundleJSON(t, map[string]string{"en/v1.0.0.md": good, "../en/v1.0.0.md": good}),
		"traversal in name":  bundleJSON(t, map[string]string{"en/v1.0.0.md": good, "en/../v1.0.0.md": good}),
		"nested path":        bundleJSON(t, map[string]string{"en/v1.0.0.md": good, "en/x/v1.0.0.md": good}),
		"absolute path":      bundleJSON(t, map[string]string{"en/v1.0.0.md": good, "/en/v1.0.0.md": good}),
		"no locale":          bundleJSON(t, map[string]string{"v1.0.0.md": good}),
		"dot locale":         bundleJSON(t, map[string]string{"en/v1.0.0.md": good, "./v1.0.0.md": good}),
		"backslash":          bundleJSON(t, map[string]string{"en/v1.0.0.md": good, `de\v1.0.0.md`: good}),
		"non-md file":        bundleJSON(t, map[string]string{"en/v1.0.0.md": good, "en/v1.0.0.txt": good}),
		"bad front-matter":   bundleJSON(t, map[string]string{"en/v1.0.0.md": "## New\n"}),
		"empty note":         bundleJSON(t, map[string]string{"en/v1.0.0.md": rawNote("v1.0.0", "2026-01-01", "\n")}),
		"too many entries":   bundleJSON(t, tooMany()),
		"trailing garbage":   append(bundleJSON(t, map[string]string{"en/v1.0.0.md": good}), []byte(`{}`)...),
		"unknown top field":  []byte(`{"format":1,"files":{"en/v1.0.0.md":` + jsonString(good) + `},"x":1}`),
		"uppercase .MD only": bundleJSON(t, map[string]string{"en/v1.0.0.MD": good}),
	}
	for name, b := range cases {
		if _, err := LoadBundle(b); err == nil {
			t.Errorf("%s: LoadBundle succeeded, want an error", name)
		}
	}
	if _, err := LoadBundle(bundleJSON(t, map[string]string{"en/v1.0.0.md": good, "pt-BR/v1.0.0.md": good})); err != nil {
		t.Errorf("a valid bundle with a region locale must load: %v", err)
	}
}

func tooMany() map[string]string {
	m := map[string]string{}
	for i := 0; i <= maxBundleEntries; i++ {
		v := fmt.Sprintf("v1.0.%d", i)
		m["en/"+v+".md"] = rawNote(v, "2026-01-01", "## New\n\n- x\n")
	}
	return m
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestIncoming_SkippedVersionsNewestFirst(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	// running < v <= offered, newest first.
	got := lib.Incoming("en", "0.30.5", "0.30.10")
	if strings.Join(versionsOf(got), ",") != "v0.30.10,v0.30.6" {
		t.Fatalf("Incoming(0.30.5→0.30.10) = %v, want [v0.30.10 v0.30.6]", versionsOf(got))
	}
	// Never the running version itself, never anything newer than offered.
	if got := lib.Incoming("en", "0.30.6", "0.30.6"); len(got) != 0 {
		t.Fatalf("Incoming(same) = %v, want none", versionsOf(got))
	}
	if got := lib.Incoming("en", "v0.4.0", "v0.30.5"); strings.Join(versionsOf(got), ",") != "v0.30.5" {
		t.Fatalf("Incoming(0.4.0→0.30.5) = %v", versionsOf(got))
	}
}

func TestIncoming_DevBuildGetsOfferedOnly(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	for _, running := range []string{"dev", "", "0.30.7-nightly.1"} {
		got := lib.Incoming("en", running, "0.30.6")
		if strings.Join(versionsOf(got), ",") != "v0.30.6" {
			t.Errorf("Incoming(%q→0.30.6) = %v, want just the offered version", running, versionsOf(got))
		}
	}
	if got := lib.Incoming("en", "dev", "dev"); len(got) != 0 {
		t.Errorf("a non-release offered version has no notes: %v", versionsOf(got))
	}
}

func TestIncoming_LocaleFallbackPerNote(t *testing.T) {
	lib, err := Load(testFS(), "rn")
	if err != nil {
		t.Fatal(err)
	}
	got := lib.Incoming("de-DE", "0.30.5", "0.30.10")
	if len(got) != 2 {
		t.Fatalf("got %v", versionsOf(got))
	}
	if got[0].Translated || got[0].Locale != "en" {
		t.Errorf("v0.30.10 has no German note: want English, Translated=false: %+v", got[0])
	}
	if !got[1].Translated || got[1].Locale != "de" {
		t.Errorf("v0.30.6 should be German: %+v", got[1])
	}
}
