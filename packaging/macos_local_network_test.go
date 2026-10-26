package packaging

// ut-docs#5026: macOS shows "find devices on local networks" the first time
// the till browses the LAN. Without NSLocalNetworkUsageDescription the dialog
// is the generic OS text; without NSBonjourServices macOS cannot say which
// services are meant. These tests pin the plist keys, the Bonjour list
// (exactly the service types internal/discovery browses or advertises) and one
// InfoPlist.strings per shipped language that build-app.sh stages.

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/discovery"
)

const localNetworkKey = "NSLocalNetworkUsageDescription"

// packLocales: the core web/locales/*.json languages plus the
// ut-plugin-language-* packs the product ships (de, es, pt).
var packLocales = []string{"de", "es", "pt"}

// plistTop returns the top-level <dict> of an XML plist as key -> node, where
// a node is the string value, or the joined <string> items of an <array>.
func plistTop(t *testing.T, path string) map[string][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := xml.NewDecoder(f)
	out := map[string][]string{}
	depth := 0
	var key string
	var inArray bool
	var cur []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "dict":
				depth++
			case "array":
				if depth == 1 {
					inArray, cur = true, nil
				}
			case "key", "string":
				var s string
				if err := dec.DecodeElement(&s, &e); err != nil {
					t.Fatal(err)
				}
				switch {
				case depth != 1:
				case e.Name.Local == "key":
					key = s
				case inArray:
					cur = append(cur, s)
				default:
					out[key] = []string{s}
				}
			}
		case xml.EndElement:
			switch e.Name.Local {
			case "dict":
				depth--
			case "array":
				if depth == 1 {
					out[key] = append([]string{}, cur...)
					if cur == nil {
						out[key] = []string{}
					}
					inArray = false
				}
			}
		}
	}
	return out
}

var stringsLine = regexp.MustCompile(`(?m)^"([A-Za-z]+)"\s*=\s*"((?:[^"\\]|\\.)*)"\s*;\s*$`)

func readStrings(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (every shipped language needs an InfoPlist.strings)", err)
	}
	if !utf8.Valid(b) {
		t.Fatalf("%s is not UTF-8", path)
	}
	m := map[string]string{}
	for _, sm := range stringsLine.FindAllStringSubmatch(string(b), -1) {
		m[sm[1]] = sm[2]
	}
	return m
}

func lprojDirs(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join("macos", "Resources"))
	if err != nil {
		t.Fatal(err)
	}
	var langs []string
	for _, e := range ents {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".lproj") {
			langs = append(langs, strings.TrimSuffix(e.Name(), ".lproj"))
		}
	}
	sort.Strings(langs)
	return langs
}

func shippedLocales(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "web", "locales", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no core locales found: %v", err)
	}
	set := map[string]bool{}
	for _, f := range files {
		set[strings.TrimSuffix(filepath.Base(f), ".json")] = true
	}
	for _, p := range packLocales {
		set[p] = true
	}
	var out []string
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func TestInfoPlistExplainsLocalNetwork(t *testing.T) {
	top := plistTop(t, filepath.Join("macos", "Info.plist"))
	v := top[localNetworkKey]
	if len(v) != 1 || strings.TrimSpace(v[0]) == "" {
		t.Fatalf("Info.plist needs a non-empty %s fallback string, got %v", localNetworkKey, v)
	}
	if got := top["CFBundleDevelopmentRegion"]; len(got) != 1 || got[0] != "en" {
		t.Errorf("CFBundleDevelopmentRegion = %v, want [en] (the fallback text is English)", got)
	}
}

// The list must be exactly what internal/discovery browses or advertises:
// every extra type widens what the app may see on the shop network.
func TestInfoPlistBonjourServicesMatchDiscovery(t *testing.T) {
	top := plistTop(t, filepath.Join("macos", "Info.plist"))
	got, ok := top["NSBonjourServices"]
	if !ok {
		t.Fatal("Info.plist has no NSBonjourServices")
	}
	want := []string{discovery.ServiceName, discovery.PrinterServiceName}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("NSBonjourServices = %v, want exactly internal/discovery's %v", got, want)
	}
}

func TestInfoPlistLocalizationsMatchLprojs(t *testing.T) {
	top := plistTop(t, filepath.Join("macos", "Info.plist"))
	got := append([]string{}, top["CFBundleLocalizations"]...)
	sort.Strings(got)
	want := shippedLocales(t)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CFBundleLocalizations = %v, want every shipped language %v", got, want)
	}
	if dirs := lprojDirs(t); strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("macos/Resources/*.lproj = %v, want %v", dirs, want)
	}
}

func TestInfoPlistStringsPerLocale(t *testing.T) {
	fb := plistTop(t, filepath.Join("macos", "Info.plist"))[localNetworkKey]
	if len(fb) != 1 {
		t.Fatalf("Info.plist has no %s", localNetworkKey)
	}
	enFallback := fb[0]
	en := readStrings(t, filepath.Join("macos", "Resources", "en.lproj", "InfoPlist.strings"))[localNetworkKey]
	if en != enFallback {
		t.Errorf("en.lproj text %q differs from the Info.plist fallback %q", en, enFallback)
	}
	seen := map[string]string{}
	for _, l := range shippedLocales(t) {
		s := readStrings(t, filepath.Join("macos", "Resources", l+".lproj", "InfoPlist.strings"))
		v := strings.TrimSpace(s[localNetworkKey])
		if v == "" {
			t.Errorf("%s: no %s", l, localNetworkKey)
			continue
		}
		if !strings.Contains(v, "Universal Till") {
			t.Errorf("%s: text should name the app: %q", l, v)
		}
		if prev, dup := seen[v]; dup {
			t.Errorf("%s: same text as %s (untranslated copy?)", l, prev)
		}
		seen[v] = l
		if (l == "ar" || l == "fa") && !regexp.MustCompile(`[\p{Arabic}]`).MatchString(v) {
			t.Errorf("%s: no Arabic-script letters", l)
		}
		if l == "fa" || l == "ar" {
			// The first strong character decides macOS's text direction: a
			// leading Latin "Universal Till" would lay the sentence out LTR.
			if r, _ := utf8.DecodeRuneInString(v); !unicode.Is(unicode.Arabic, r) {
				t.Errorf("%s: must start with an Arabic-script word, not %q", l, r)
			}
		}
		if n := utf8.RuneCountInString(v); n > 200 {
			t.Errorf("%s: %d characters is too long for a system dialog", l, n)
		}
		if strings.Contains(v, "%") || strings.Contains(v, "\\n") {
			t.Errorf("%s: no placeholders or line breaks in a usage text: %q", l, v)
		}
	}
}

func TestInfoPlistStringsLint(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS-only")
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	files, _ := filepath.Glob(filepath.Join("macos", "Resources", "*.lproj", "InfoPlist.strings"))
	args := append([]string{"-lint", filepath.Join("macos", "Info.plist")}, files...)
	if out, err := exec.Command("plutil", args...).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint: %v\n%s", err, out)
	}
}

func TestBuildAppStagesLprojs(t *testing.T) {
	lines := buildScriptLinesNoComments(t)
	// The trailing "/." matters: without it cp nests Resources/Resources/*.lproj.
	const cp = `cp -R packaging/macos/Resources/. "$APP/Contents/Resources/"`
	at := -1
	for _, l := range strings.Split(lines, "\n") {
		if strings.TrimSpace(l) == cp {
			at = strings.Index(lines, l)
		}
	}
	if at < 0 {
		t.Fatalf("build-app.sh must contain the line %s", cp)
	}
	if at > strings.Index(lines, "codesign ") {
		t.Fatal("lproj staging must come before codesign so the bundle seal covers it")
	}
}

func buildScriptLinesNoComments(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("macos", "build-app.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}
