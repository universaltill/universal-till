package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins/bundled"
)

// ut-docs#7017: a broken code plugin on the App Store build says it can't run.

func seedBroken7017(t *testing.T, d *common.Deps, id, runtime, state string) {
	t.Helper()
	seedTestPlugin(t, d.Db, id, "Broken Plugin", "1.0.0")
	if _, err := d.Db.Exec(`UPDATE plugins SET runtime=?, install_state=? WHERE id=?`, runtime, state, id); err != nil {
		t.Fatal(err)
	}
}

func TestPluginsPage_CodeRefusedFlag_7017(t *testing.T) {
	cases := []struct {
		name    string
		channel buildinfo.InstallChannel
		runtime string
		state   string
		want    bool
	}{
		{"appstore broken wasm", buildinfo.ChannelAppStore, "wasm", "broken", true},
		{"appstore broken go", buildinfo.ChannelAppStore, "go", "broken", true},
		{"direct broken wasm", buildinfo.ChannelDirect, "wasm", "broken", false},
		// Empty runtime fails closed like the install gate (CodeRuntimeRefused):
		// a legacy row with no runtime is treated as code on a code-free build.
		{"appstore broken empty runtime", buildinfo.ChannelAppStore, "", "broken", true},
		{"appstore broken none", buildinfo.ChannelAppStore, "none", "broken", false},
		{"appstore active wasm", buildinfo.ChannelAppStore, "wasm", "active", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer buildinfo.OverrideChannelForTest(c.channel)()
			d := pluginsManagerTestDeps(t)
			seedBroken7017(t, d, "com.x.b", c.runtime, c.state)
			body, items := getPluginsPage7017(t, d)
			if got := items["com.x.b"].CodeRefused; got != c.want {
				t.Fatalf("codeRefused = %v, want %v", got, c.want)
			}
			_ = body // markup is always rendered (x-show toggles it client-side); the JSON flag is the signal
		})
	}
}

// Alpine-bound :title/:aria-label must not embed a {{ T }} call inside a JS
// string: html/template escapes ' to &#39;, the browser decodes it, and the
// expression becomes a syntax error (ut-docs#7017).
func TestPluginsHTML_NoTranslatedStringInBoundAttr_7017(t *testing.T) {
	b, err := os.ReadFile("web/ui/pages/plugins.html")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`:(title|aria-label)="[^"]*\{\{\s*T\s`)
	if m := re.FindString(string(b)); m != "" {
		t.Fatalf("Alpine-bound attribute embeds a T call in a JS string: %s", m)
	}
}

type codeRefusedItem7017 struct {
	ID          string `json:"id"`
	CodeRefused bool   `json:"codeRefused"`
}

func getPluginsPage7017(t *testing.T, d *common.Deps) (string, map[string]codeRefusedItem7017) {
	t.Helper()
	mux := http.NewServeMux()
	registerPluginsPage(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugins = %d", rec.Code)
	}
	const open = `<script id="plugins-data" type="application/json">`
	start := strings.Index(body, open)
	if start < 0 {
		t.Fatal("plugins-data missing")
	}
	rest := body[start+len(open):]
	rest = rest[:strings.Index(rest, "</script>")]
	var payload struct {
		Items []codeRefusedItem7017 `json:"items"`
	}
	if err := json.Unmarshal([]byte(rest), &payload); err != nil {
		t.Fatal(err)
	}
	m := map[string]codeRefusedItem7017{}
	for _, it := range payload.Items {
		m[it.ID] = it
	}
	return body, m
}

// AC5: a bundled plugin (system trust, in the bundled lock) is never flagged.
func TestPluginsPage_CodeRefusedFlag_BundledNotFlagged_7017(t *testing.T) {
	ids := bundled.IDs()
	if len(ids) == 0 {
		t.Skip("no bundled lock")
	}
	defer buildinfo.OverrideChannelForTest(buildinfo.ChannelAppStore)()
	defer bundled.OverrideAvailableForTest(true)()
	d := pluginsManagerTestDeps(t)
	seedBroken7017(t, d, ids[0], "wasm", "broken")
	if _, err := d.Db.Exec(`UPDATE plugins SET trust_level='system' WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	_, items := getPluginsPage7017(t, d)
	if items[ids[0]].CodeRefused {
		t.Fatal("bundled broken plugin must not be codeRefused")
	}
	// control: same row not system-trusted is flagged, proving the test bites.
	if _, err := d.Db.Exec(`UPDATE plugins SET trust_level='community' WHERE id=?`, ids[0]); err != nil {
		t.Fatal(err)
	}
	_, items = getPluginsPage7017(t, d)
	if !items[ids[0]].CodeRefused {
		t.Fatal("control: non-system row should be codeRefused")
	}
}
