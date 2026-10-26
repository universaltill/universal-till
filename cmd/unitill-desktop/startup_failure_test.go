package main

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/web/locales"
)

func TestShellLang(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name     string
		env      map[string]string
		platform string
		want     string
	}{
		{"LC_ALL wins", map[string]string{"LC_ALL": "tr_TR.UTF-8", "LANG": "de_DE"}, "ar", "tr"},
		{"LC_MESSAGES before LANG", map[string]string{"LC_MESSAGES": "fa_IR", "LANG": "de_DE"}, "", "fa"},
		{"LANG", map[string]string{"LANG": "en_US.UTF-8"}, "tr", "en"},
		{"hyphen region", map[string]string{"LANG": "ar-EG"}, "", "ar"},
		{"C ignored, next used", map[string]string{"LC_ALL": "C", "LANG": "tr_TR"}, "", "tr"},
		{"POSIX ignored", map[string]string{"LC_ALL": "POSIX"}, "fa-IR", "fa"},
		{"C.UTF-8 ignored", map[string]string{"LANG": "C.UTF-8"}, "tr-TR", "tr"},
		{"platform fallback", nil, "tr-TR", "tr"},
		{"platform underscore", nil, "ar_SA", "ar"},
		{"default en", nil, "", "en"},
	}
	for _, c := range cases {
		if got := shellLang(env(c.env), c.platform); got != c.want {
			t.Errorf("%s: shellLang = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLoadShellTranslator(t *testing.T) {
	en := loadShellTranslator(locales.FS, "en")
	tr := loadShellTranslator(locales.FS, "tr")
	if got := en("desktop.startup_failed.title"); got != "Universal Till could not start" {
		t.Fatalf("en title = %q", got)
	}
	if got := tr("desktop.startup_failed.title"); got == en("desktop.startup_failed.title") || got == "" {
		t.Fatalf("tr title not translated: %q", got)
	}
	if got := en("no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key = %q, want the key", got)
	}
}

func TestLoadShellTranslatorOverlayAndFallback(t *testing.T) {
	mem := fstestMap{
		"en.json": `{"a":"A-en","b":"B-en"}`,
		"xx.json": `{"a":"A-xx","b":""}`,
	}
	tr := loadShellTranslator(mem, "xx")
	if tr("a") != "A-xx" {
		t.Errorf("overlay not applied: %q", tr("a"))
	}
	if tr("b") != "B-en" {
		t.Errorf("empty overlay value must fall back to English: %q", tr("b"))
	}
}

func TestLoadShellTranslatorRejectsBadLangCode(t *testing.T) {
	mem := &recordingFS{files: map[string]string{
		"en.json":   `{"a":"A-en"}`,
		"../x.json": `{"a":"EVIL"}`,
		"x.json":    `{"a":"EVIL"}`,
	}}
	for _, lang := range []string{"../x", "x/y", "EN-us", "toolonglang", "a.b", ""} {
		tr := loadShellTranslator(mem, lang)
		if tr("a") != "A-en" {
			t.Errorf("lang %q: got %q", lang, tr("a"))
		}
	}
	for _, name := range mem.opened {
		if name != "en.json" {
			t.Errorf("unexpected file opened: %q", name)
		}
	}
}

func TestStartupFailurePage(t *testing.T) {
	for _, lang := range []string{"en", "tr", "ar", "fa"} {
		tr := loadShellTranslator(locales.FS, lang)
		page := startupFailurePage(tr, lang, `/data/Universal Till`, "signal: killed")
		for _, k := range startupFailureKeys {
			want := htmlEscapeForTest(tr(k))
			if !strings.Contains(page, want) {
				t.Errorf("%s: page lacks %s (%q)", lang, k, want)
			}
		}
		if !strings.Contains(page, "<code") || !strings.Contains(page, "/data/Universal Till") {
			t.Errorf("%s: data folder not shown in <code>", lang)
		}
		if !strings.Contains(page, "signal: killed") {
			t.Errorf("%s: reason missing", lang)
		}
		if strings.Contains(page, "http://") || strings.Contains(page, "https://") || strings.Contains(page, "<script") {
			t.Errorf("%s: page must be self-contained", lang)
		}
		wantDir := "ltr"
		if lang == "ar" || lang == "fa" {
			wantDir = "rtl"
		}
		if !strings.Contains(page, `dir="`+wantDir+`"`) || !strings.Contains(page, `lang="`+lang+`"`) {
			t.Errorf("%s: want lang/dir attributes (dir=%s)", lang, wantDir)
		}
	}
}

func TestStartupFailurePageEscapes(t *testing.T) {
	tr := loadShellTranslator(locales.FS, "en")
	page := startupFailurePage(tr, "en", `/x/<script>alert(1)</script>`, `<script>boom</script>`)
	if strings.Contains(page, "<script") {
		t.Fatalf("unescaped script in page:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatal("expected escaped markup")
	}
}

func TestStartupFailurePageOmitsEmptyReason(t *testing.T) {
	tr := loadShellTranslator(locales.FS, "en")
	page := startupFailurePage(tr, "en", "/d", "")
	if strings.Contains(page, tr("desktop.startup_failed.detail")) {
		t.Fatal("detail line must be omitted when the reason is empty")
	}
	with := startupFailurePage(tr, "en", "/d", "exit status 1")
	if !strings.Contains(with, tr("desktop.startup_failed.detail")) {
		t.Fatal("detail line missing when a reason is given")
	}
}

func TestStartupFailurePageUsesLogicalCSSOnly(t *testing.T) {
	tr := loadShellTranslator(locales.FS, "en")
	page := strings.ToLower(startupFailurePage(tr, "en", "/d", "r"))
	i, j := strings.Index(page, "<style>"), strings.Index(page, "</style>")
	if i < 0 || j < i {
		t.Fatal("no inline <style> block")
	}
	page = page[i:j] // the CSS only: the English prose may say "right after"
	for _, bad := range []string{"left", "right"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains physical direction %q", bad)
		}
	}
}

func TestStartupFailureKeysExistInEveryLocale(t *testing.T) {
	entries, err := fs.ReadDir(locales.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		n++
		raw, err := fs.ReadFile(locales.FS, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		for _, k := range startupFailureKeys {
			if m[k] == "" {
				t.Errorf("%s lacks %s", e.Name(), k)
			}
		}
	}
	if n < 4 {
		t.Fatalf("only %d locales embedded", n)
	}
}

func TestAvailableShellLang(t *testing.T) {
	cases := map[string]string{"tr": "tr", "fa": "fa", "en": "en", "he": "en", "xx": "en", "../en": "en", "": "en"}
	for in, want := range cases {
		if got := availableShellLang(locales.FS, in); got != want {
			t.Errorf("availableShellLang(%q) = %q, want %q", in, got, want)
		}
	}
}
