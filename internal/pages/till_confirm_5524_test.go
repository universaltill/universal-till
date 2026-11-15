package pages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ut-docs#5524: hx-confirm opens the till's own in-page dialog
// (web/public/till-confirm.js) instead of the browser's window.confirm.

func enLocale(t *testing.T) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := json.Unmarshal([]byte(readRepoFile(t, "web/locales/en.json")), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBase_TillConfirmShell(t *testing.T) {
	base := readRepoFile(t, "web/ui/layouts/base.html")
	if n := strings.Count(base, `<dialog id="ut-confirm"`); n != 1 {
		t.Fatalf("base.html must carry exactly one #ut-confirm dialog, found %d", n)
	}
	i := strings.Index(base, `<dialog id="ut-confirm"`)
	j := strings.Index(base[i:], "</dialog>")
	if j < 0 {
		t.Fatal("#ut-confirm is not closed")
	}
	dlg := base[i : i+j]
	open := dlg[:strings.Index(dlg, ">")+1]
	for _, want := range []string{"data-ut-focus-trap", "confirm-dialog", "modifier-modal"} {
		if !strings.Contains(open, want) {
			t.Errorf("#ut-confirm opening tag lacks %q: %s", want, open)
		}
	}
	if regexp.MustCompile(`\sopen[\s>=]`).MatchString(open) {
		t.Errorf("#ut-confirm must ship closed: %s", open)
	}
	if strings.Contains(open, "data-ut-escape-close") {
		t.Errorf("#ut-confirm handles Escape itself, not via data-ut-escape-close: %s", open)
	}
	if strings.Contains(dlg, "<script") || strings.Contains(dlg, "<form") {
		t.Error("#ut-confirm must hold no script and no form")
	}
	for _, want := range []string{
		`data-ut-confirm="cancel"`, `data-ut-confirm="ok"`,
		`{{ T "common.cancel" }}`, `{{ T "common.confirm" }}`,
		`data-default-label="{{ T "common.confirm" }}"`,
		`data-default-label="{{ T "common.cancel" }}"`,
		`id="ut-confirm-msg"`, `aria-labelledby="ut-confirm-msg"`,
	} {
		if !strings.Contains(dlg, want) {
			t.Errorf("#ut-confirm lacks %s", want)
		}
	}
	if strings.Index(base, `<dialog id="confirm-modal"`) > i {
		t.Error("#ut-confirm must come after the #confirm-modal placeholder")
	}
	if enLocale(t)["common.confirm"] == "" {
		t.Error("en.json lacks common.confirm")
	}
	meta := readRepoFile(t, "web/i18n-meta/en.meta.json")
	if !strings.Contains(meta, `"common.confirm"`) {
		t.Error("en.meta.json lacks common.confirm")
	}
}

func TestBase_LoadsTillConfirmJS(t *testing.T) {
	base := readRepoFile(t, "web/ui/layouts/base.html")
	app := strings.Index(base, `src="/public/app.js?v=`)
	tc := strings.Index(base, `<script defer src="/public/till-confirm.js?v={{ assetv "public/till-confirm.js" }}"></script>`)
	if tc < 0 {
		t.Fatal("base.html does not load public/till-confirm.js")
	}
	if app < 0 || tc < app {
		t.Error("till-confirm.js must load after app.js")
	}
	js := readRepoFile(t, "web/public/till-confirm.js")
	for _, bad := range []string{"showModal", "innerHTML"} {
		if strings.Contains(js, bad) {
			t.Errorf("till-confirm.js must not use %s", bad)
		}
	}
}

// The neutral-tone list is a conscious decision (UX D2): pin it.
func TestHxConfirm_NeutralToneSites(t *testing.T) {
	want := []string{"tills.network_key.confirm", "tills.role.change_confirm", "tills.update_now.confirm"}
	tag := regexp.MustCompile(`(?s)<[a-zA-Z][^<>]*?data-confirm-tone="([^"]*)"[^<>]*>`)
	hx := regexp.MustCompile(`hx-confirm="\{\{[^}]*?\(?T\s+"([^"]+)"`)
	var got []string
	err := filepath.Walk("web/ui", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		b, _ := os.ReadFile(p)
		for _, m := range tag.FindAllStringSubmatch(string(b), -1) {
			if m[1] != "neutral" {
				t.Errorf("%s: data-confirm-tone must be \"neutral\", got %q", p, m[1])
			}
			k := hx.FindStringSubmatch(m[0])
			if k == nil {
				t.Errorf("%s: data-confirm-tone element has no hx-confirm=\"{{ T ... }}\": %s", p, m[0])
				continue
			}
			got = append(got, k[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("neutral-tone sites = %v, want exactly %v", got, want)
	}
}
