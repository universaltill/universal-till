package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#2892: a plugin's content/index.html used to be injected raw into
// the till's own document (same origin, the manager's session). It must now
// render only inside an iframe with an EMPTY sandbox token list, carried in
// an attribute-escaped srcdoc.

var (
	iframeTagRe = regexp.MustCompile(`(?s)<iframe\b[^>]*>`)
	srcdocRe    = regexp.MustCompile(`(?s)\ssrcdoc="([^"]*)"`)
	sandboxRe   = regexp.MustCompile(`\ssandbox(="")?[\s>]`)
)

func servePluginStaticPage(t *testing.T, indexHTML, query string) *httptest.ResponseRecorder {
	t.Helper()
	d, base := pluginPageTestDeps(t)
	seedTestPlugin(t, d.Db, "com.x.docs", "Docs", "1.0.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.docs','page','docs','/plugin/docs','Docs Page')`); err != nil {
		t.Fatal(err)
	}
	contentDir := filepath.Join(base, "com.x.docs", "1.0.0", "content")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contentDir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugin/docs"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /plugin/docs = %d", rec.Code)
	}
	return rec
}

// pluginFrame returns the single plugin iframe's opening tag and its
// unescaped srcdoc document.
func pluginFrame(t *testing.T, body string) (tag, doc string) {
	t.Helper()
	tags := iframeTagRe.FindAllString(body, -1)
	if len(tags) != 1 {
		t.Fatalf("want exactly one <iframe> in the host page, got %d", len(tags))
	}
	m := srcdocRe.FindStringSubmatch(tags[0])
	if m == nil {
		t.Fatalf("plugin iframe has no srcdoc: %s", tags[0])
	}
	return tags[0], html.UnescapeString(m[1])
}

func TestPluginPage_StaticHTMLIsSandboxedInSrcdoc(t *testing.T) {
	const hostile = `<h2>Plugin docs</h2><script>alert("pwn")</script><img src=x onerror="alert('img')">`
	rec := servePluginStaticPage(t, hostile, "")
	body := rec.Body.String()

	tag, doc := pluginFrame(t, body)
	if !sandboxRe.MatchString(tag) {
		t.Errorf("plugin iframe lacks a sandbox attribute: %s", tag)
	}
	if strings.Contains(tag, "allow-") {
		t.Errorf("plugin iframe sandbox must have an EMPTY token list (no allow-*): %s", tag)
	}

	// The host document must carry none of the plugin's active markup raw.
	for _, raw := range []string{`<script>alert("pwn")`, `onerror="alert('img')"`, `<h2>Plugin docs</h2>`} {
		if strings.Contains(body, raw) {
			t.Errorf("host document contains raw plugin markup %q", raw)
		}
	}
	// ...it lives, escaped, inside the frame's srcdoc document.
	for _, want := range []string{`<h2>Plugin docs</h2>`, `<script>alert("pwn")</script>`, `onerror="alert('img')"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("srcdoc document missing %q", want)
		}
	}

	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"object-src 'none'", "frame-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy %q missing %q", csp, want)
		}
	}
}

func TestPluginPage_StaticDocsRenderTextAndLocale(t *testing.T) {
	// tax-uk-shaped static docs: headings, lists, <pre>, <strong>.
	const docs = `<h2>What this does</h2><p>UK VAT law treats most <strong>cold food</strong> differently.</p><pre>{"tax_zero": 2000}</pre>`
	rec := servePluginStaticPage(t, docs, "?lang=fa")
	_, doc := pluginFrame(t, rec.Body.String())
	for _, want := range []string{"What this does", "<strong>cold food</strong>", `{"tax_zero": 2000}`,
		`dir="rtl"`, `lang="fa"`, `/public/app.css`} {
		if !strings.Contains(doc, want) {
			t.Errorf("srcdoc document missing %q:\n%s", want, doc)
		}
	}
	// The frame document is served in the till's theme.
	if !strings.Contains(doc, `/themes/monarch.css`) {
		t.Errorf("srcdoc document does not load the shop theme:\n%s", doc)
	}
}

func TestPluginPage_ContentBundleEscapesEveryField(t *testing.T) {
	d, base := pluginPageTestDeps(t)
	seedTestPlugin(t, d.Db, "com.x.faq", "FAQ Plugin", "1.2.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES('e1','com.x.faq','page','faq-page','/plugin/faq','Help / FAQ')`); err != nil {
		t.Fatal(err)
	}
	contentDir := filepath.Join(base, "com.x.faq", "1.2.0", "content")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := `{"locale":"en-US","version":"<script>v()</script>","rtl":false,
		"categories":[{"id":"g","name":"<script>cat()</script>","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"g","question":"<script>q()</script>",
		  "answer":"<img src=x onerror=a()>","sort_order":1,"last_updated":"<b>2026</b>",
		  "keywords":["\"><script>k()</script>"]}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugin/faq", nil))
	body := rec.Body.String()
	for _, raw := range []string{"<script>v()", "<script>cat()", "<script>q()", "<img src=x onerror", "<b>2026</b>", "<script>k()"} {
		if strings.Contains(body, raw) {
			t.Errorf("content bundle field rendered unescaped: %q", raw)
		}
	}
	if !strings.Contains(body, "&lt;script&gt;q()&lt;/script&gt;") {
		t.Error("escaped question text missing from bundle page")
	}
}

func assertPluginPageCSP(t *testing.T, branch string, rec *httptest.ResponseRecorder) {
	t.Helper()
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"object-src 'none'", "frame-src 'self' about:"} {
		if !strings.Contains(csp, want) {
			t.Errorf("%s branch: Content-Security-Policy %q missing %q", branch, csp, want)
		}
	}
}

// Security review (ut-docs#2892): the plugin page CSP must be on every
// branch a plugin page route can take, not only the static-HTML one.
func TestPluginPage_CSPOnEveryBranch(t *testing.T) {
	// static content/index.html
	assertPluginPageCSP(t, "static", servePluginStaticPage(t, "<p>x</p>", ""))

	// content bundle
	d, base := pluginPageTestDeps(t)
	seedTestPlugin(t, d.Db, "com.x.faq", "FAQ Plugin", "1.2.0")
	seedTestPlugin(t, d.Db, "com.x.bare", "Bare", "0.1.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label) VALUES
		('e1','com.x.faq','page','faq-page','/plugin/faq','FAQ'),
		('e2','com.x.bare','page','bare','/plugin/bare','Bare')`); err != nil {
		t.Fatal(err)
	}
	contentDir := filepath.Join(base, "com.x.faq", "1.2.0", "content")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bundle := `{"locale":"en-US","rtl":false,"categories":[{"id":"g","name":"G","sort_order":1}],
		"faq_entries":[{"id":"q1","category":"g","question":"Q?","answer":"A.","sort_order":1}]}`
	if err := os.WriteFile(filepath.Join(contentDir, "en-US.json"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerPluginPages(mux, d)
	for route, branch := range map[string]string{"/plugin/faq": "bundle", "/plugin/bare": "info-card"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", route, rec.Code)
		}
		assertPluginPageCSP(t, branch, rec)
	}
}

// The plugin's content language need not match the till locale: an English
// docs page in an fa till must not be mirrored. lang/dir on <html> follow
// the locale; the body takes its direction from its own text.
func TestPluginPage_FrameBodyDirIsAuto(t *testing.T) {
	rec := servePluginStaticPage(t, "<h2>English docs</h2>", "?lang=fa")
	_, doc := pluginFrame(t, rec.Body.String())
	bodyTag := regexp.MustCompile(`<body\b[^>]*>`).FindString(doc)
	if !strings.Contains(bodyTag, `dir="auto"`) {
		t.Errorf("frame <body> must carry dir=\"auto\", got %q", bodyTag)
	}
	if !strings.Contains(doc, `lang="fa"`) {
		t.Errorf("frame document lost the locale lang:\n%s", doc)
	}
}
