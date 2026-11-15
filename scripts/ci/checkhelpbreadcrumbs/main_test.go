package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseItems(t *testing.T) {
	src := "---\nid: x\n---\n\n# H\n\n1. one\n   more\n\n2. two\n- bullet\n3. three\n"
	got := parseItems(src)
	if len(got) != 3 {
		t.Fatalf("items = %d, want 3", len(got))
	}
	if got[0].line != 7 || !strings.Contains(got[0].text, "more") {
		t.Errorf("item 1 = %+v", got[0])
	}
	if strings.Contains(got[1].text, "bullet") || got[2].line != 12 {
		t.Errorf("item 2/3 wrong: %+v %+v", got[1], got[2])
	}
}

func TestLabelMapPrefersFewestDotsThenLowest(t *testing.T) {
	m := labelMap(map[string]string{
		"settings.a.b.title":  "Dup",
		"settings.z.title":    "Dup",
		"settings.b.title":    "Dup",
		"other.title":         "Nope",
		"settings.x.hint":     "Nope2",
		"settings.long.title": strings.Repeat("x", 41),
	})
	if m["Dup"] != "settings.b.title" {
		t.Errorf("Dup -> %q", m["Dup"])
	}
	if len(m) != 1 {
		t.Errorf("map = %v", m)
	}
}

func TestLabelMapExtraKeys(t *testing.T) {
	tests := []struct {
		name string
		en   map[string]string
		want map[string]string
	}{
		{"extra keys", map[string]string{
			"settings.all":                       "All Settings",
			"settings.barcode.symbologies_title": "Barcode types",
			"tills.title":                        "Tills",
			"menulayout.title":                   "Hidden menu tiles",
			"page.title.tills":                   "Nope",
			"settings.other_title":               "Nope2",
			"settings.x.hint":                    "Nope3",
		}, map[string]string{
			"All Settings":      "settings.all",
			"Barcode types":     "settings.barcode.symbologies_title",
			"Tills":             "tills.title",
			"Hidden menu tiles": "menulayout.title",
		}},
		{"primary beats extra", map[string]string{
			"tills.title":           "Dup",
			"settings.deep.b.title": "Dup",
		}, map[string]string{"Dup": "settings.deep.b.title"}},
		{"primary beats extra either order", map[string]string{
			"settings.z.label": "Dup",
			"settings.all":     "Dup",
		}, map[string]string{"Dup": "settings.z.label"}},
		{"extra tier fewest dots then lowest", map[string]string{
			"settings.barcode.symbologies_title": "Dup",
			"tills.title":                        "Dup",
			"settings.all":                       "Dup",
		}, map[string]string{"Dup": "settings.all"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := labelMap(tc.en)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%q -> %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestFindBreadcrumbsLongestAndBoundary(t *testing.T) {
	labels := map[string]string{"Data": "settings.data", "Data management": "settings.dm", "Display": "settings.display"}
	got := findBreadcrumbs("see Settings → Data management and **Settings → **Display** then Settings → Datasheet, Settings → Data.", labels)
	var keys []string
	for _, b := range got {
		keys = append(keys, b.key)
	}
	want := "settings.dm,settings.display,settings.data"
	if strings.Join(keys, ",") != want {
		t.Errorf("keys = %v, want %s", keys, want)
	}
}

func TestCheckTopic(t *testing.T) {
	labels := map[string]string{"Display": "settings.display.title"}
	en := map[string]string{"settings.display.title": "Display"}
	loc := map[string]string{"settings.display.title": "Görünüm"}
	enSrc := "1. Open Settings → Display.\n"
	if f, _ := checkTopic("tr", "t.md", "p", enSrc, "1. Ayarlar → Görünüm'de açın.\n", labels, loc, en); len(f) != 0 {
		t.Errorf("suffix should pass: %v", f)
	}
	f, _ := checkTopic("tr", "t.md", "web/help/tr/t.md", enSrc, "x\n\n1. Ayarlar → Ekran.\n", labels, loc, en)
	if len(f) != 1 || f[0].line != 3 || f[0].expected != "Görünüm" || f[0].id() != "tr/t.md:1:settings.display.title" {
		t.Errorf("got %+v", f)
	}
	if f, aligned := checkTopic("tr", "t.md", "p", enSrc, "1. a\n2. b\n", labels, loc, en); len(f) != 0 || aligned {
		t.Errorf("count mismatch should skip unaligned: %v %v", f, aligned)
	}
	// locale lacking the key falls back to the English value
	if f, _ := checkTopic("tr", "t.md", "p", enSrc, "1. Settings → Display\n", labels, map[string]string{}, en); len(f) != 0 {
		t.Errorf("fallback: %v", f)
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunBaselineAndSkip(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web/locales/en.json", `{"settings.display.title":"Display"}`)
	write(t, root, "web/locales/tr.json", `{"settings.display.title":"Görünüm"}`)
	write(t, root, "web/help/en/t.md", "1. Settings → Display\n")
	write(t, root, "web/help/tr/t.md", "1. Ayarlar → Ekran\n")
	write(t, root, "web/help/de/t.md", "1. x\n")
	write(t, root, "web/help/img/keep.txt", "")

	var out, errb bytes.Buffer
	if rc := run(root, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "web/help/tr/t.md:1:") {
		t.Fatalf("rc=%d err=%s", rc, errb.String())
	}
	if !strings.Contains(out.String(), "skipping locale de") {
		t.Errorf("no de notice: %s", out.String())
	}
	write(t, root, baselineRel, "# c\ntr/t.md:1:settings.display.title because\n")
	out.Reset()
	errb.Reset()
	if rc := run(root, &out, &errb); rc != 0 {
		t.Fatalf("baselined rc=%d err=%s", rc, errb.String())
	}
	write(t, root, baselineRel, "tr/t.md:1:settings.display.title r\ntr/t.md:2:settings.display.title r\n")
	errb.Reset()
	if rc := run(root, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "stale baseline entry tr/t.md:2") {
		t.Fatalf("stale rc=%d err=%s", rc, errb.String())
	}
	// an entry for a locale this run can't load (pack not checked out) is
	// not stale: it is only judged where the pack is present
	write(t, root, baselineRel, "tr/t.md:1:settings.display.title r\nde/t.md:1:settings.display.title pack-only\n")
	errb.Reset()
	if rc := run(root, &out, &errb); rc != 0 {
		t.Fatalf("skipped-locale entry rc=%d err=%s", rc, errb.String())
	}
	// an unaligned pair is reported, not counted as checked
	write(t, root, "web/help/en/u.md", "1. a\n2. b\n")
	write(t, root, "web/help/tr/u.md", "1. a\n")
	out.Reset()
	if rc := run(root, &out, &errb); rc != 0 || !strings.Contains(out.String(), "web/help/tr/u.md numbered items don't line up") || !strings.Contains(out.String(), "1 translated topics checked (1 not aligned") {
		t.Fatalf("unaligned rc=%d out=%s", rc, out.String())
	}
	write(t, root, baselineRel, "garbage\n")
	errb.Reset()
	if rc := run(root, &out, &errb); rc != 1 || !strings.Contains(errb.String(), "malformed") {
		t.Fatalf("garbled rc=%d err=%s", rc, errb.String())
	}
}
