package help_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/help"
)

// mdTopic builds a minimal front-mattered topic file for MapFS fixtures.
func mdTopic(id, title, section string, order int, keywords []string, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + id + "\n")
	b.WriteString("title: \"" + title + "\"\n")
	b.WriteString("section: \"" + section + "\"\n")
	b.WriteString("order: " + itoa(order) + "\n")
	b.WriteString("routes: []\n")
	b.WriteString("keywords: [" + strings.Join(keywords, ", ") + "]\n")
	b.WriteString("---\n\n")
	b.WriteString(body)
	return []byte(b.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func fixtureFS() fstest.MapFS {
	return fstest.MapFS{
		"en/quick-start.md": {Data: mdTopic("quick-start", "Quick start", "Getting started", 0,
			[]string{"login", "pin", "day"},
			"Turn the till on and type your PIN.\n\n1. Scan a barcode.\n2. Take the money.\n")},
		"en/sell.md": {Data: mdTopic("sell", "Selling & checkout", "Selling", 10,
			[]string{"barcode", "checkout", "basket"},
			"The main register screen.\n\n1. Scan a barcode, or search.\n2. Choose Pay.\n")},
		"en/printing.md": {Data: mdTopic("printing", "Printing", "Selling", 11,
			[]string{"receipt", "thermal"},
			"Prints receipts and reports on a thermal printer.\n")},
		"fa/sell.md": {Data: mdTopic("sell", "فروش و تسویه", "فروش", 10,
			[]string{"بارکد"},
			"صفحه اصلی فروش.\n")},
	}
}

func TestLoadTopicsParsesFrontMatterAndMarkdown(t *testing.T) {
	idx, err := help.LoadTopics(fixtureFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	top := idx.ByID["en"]["sell"]
	if top == nil {
		t.Fatalf("en/sell not indexed")
	}
	if top.Title != "Selling & checkout" || top.Section != "Selling" || top.Order != 10 || top.Lang != "en" {
		t.Fatalf("front matter mis-parsed: %+v", top)
	}
	if len(top.Keywords) != 3 || top.Keywords[0] != "barcode" {
		t.Fatalf("keywords mis-parsed: %v", top.Keywords)
	}
	html := string(top.HTML)
	if !strings.Contains(html, "<ol>") || !strings.Contains(html, "<li>") {
		t.Fatalf("markdown ordered list not rendered to HTML: %s", html)
	}
	if strings.Contains(html, "---") || strings.Contains(html, "id: sell") {
		t.Fatalf("front matter leaked into rendered HTML: %s", html)
	}
	if fa := idx.ByID["fa"]["sell"]; fa == nil || fa.Title != "فروش و تسویه" {
		t.Fatalf("fa topic not indexed: %+v", fa)
	}
}

func TestLoadTopicsOrdersSectionsAndTopics(t *testing.T) {
	idx, err := help.LoadTopics(fixtureFS())
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	secs := idx.Sections("en")
	if len(secs) != 2 {
		t.Fatalf("sections = %d, want 2 (%+v)", len(secs), secs)
	}
	// "Getting started" holds order 0, so it sorts before "Selling".
	if secs[0].Name != "Getting started" || secs[1].Name != "Selling" {
		t.Fatalf("section order wrong: %s, %s", secs[0].Name, secs[1].Name)
	}
	if secs[1].Topics[0].ID != "sell" || secs[1].Topics[1].ID != "printing" {
		t.Fatalf("topic order within section wrong: %s, %s", secs[1].Topics[0].ID, secs[1].Topics[1].ID)
	}
}

func TestLoadTopicsMalformedFrontMatterFails(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"no front matter": {
			"en/x.md": {Data: []byte("just a body, no front matter\n")},
		},
		"unterminated front matter": {
			"en/x.md": {Data: []byte("---\nid: x\ntitle: X\nsection: S\n\nbody\n")},
		},
		"invalid yaml": {
			"en/x.md": {Data: []byte("---\nid: [unclosed\n---\nbody\n")},
		},
		"missing id": {
			"en/x.md": {Data: mdTopic("", "X", "S", 1, nil, "body")},
		},
		"missing title": {
			"en/x.md": {Data: mdTopic("x", "", "S", 1, nil, "body")},
		},
	}
	for name, fsys := range cases {
		if _, err := help.LoadTopics(fsys); err == nil {
			t.Errorf("%s: LoadTopics succeeded, want error", name)
		} else if !strings.Contains(err.Error(), "en/x.md") {
			t.Errorf("%s: error %q does not name the offending file", name, err)
		}
	}
}

func TestLoadTopicsDuplicateIDFails(t *testing.T) {
	fsys := fstest.MapFS{
		"en/a.md": {Data: mdTopic("dup", "A", "S", 1, nil, "a")},
		"en/b.md": {Data: mdTopic("dup", "B", "S", 2, nil, "b")},
	}
	_, err := help.LoadTopics(fsys)
	if err == nil {
		t.Fatalf("LoadTopics succeeded on duplicate id, want error")
	}
	if !strings.Contains(err.Error(), "dup") {
		t.Fatalf("duplicate-id error %q does not name the id", err)
	}
}

// Topic.HTML is handed to templates as template.HTML (unescaped), so the
// renderer MUST stay on goldmark's safe default — raw HTML in a topic file is
// dropped, not passed through. This pins that: adding html.WithUnsafe() (or
// any renderer option that enables raw HTML) turns every topic file into an
// injection vector the moment topics are ever authored by anyone but us.
func TestRenderedTopicHTMLDropsRawHTML(t *testing.T) {
	fsys := fstest.MapFS{
		"en/x.md": {Data: []byte("---\nid: x\ntitle: X\nsection: S\n---\n\n" +
			"<script>alert(1)</script>\n\nInline <img src=x onerror=alert(1)> here.\n")},
	}
	idx, err := help.LoadTopics(fsys)
	if err != nil {
		t.Fatalf("LoadTopics: %v", err)
	}
	html := string(idx.ByID["en"]["x"].HTML)
	for _, bad := range []string{"<script", "<img", "onerror"} {
		if strings.Contains(html, bad) {
			t.Fatalf("raw HTML %q survived rendering — the goldmark renderer is no longer safe: %s", bad, html)
		}
	}
	// The surrounding Markdown still renders, so this isn't passing by
	// accident on an empty body.
	if !strings.Contains(html, "here.") {
		t.Fatalf("markdown body did not render at all: %s", html)
	}
}

// Acceptance criterion 4: the tree is derived from the files — adding a
// topic file adds a topic with no code change.
func TestIndexReflectsWhateverFilesExist(t *testing.T) {
	small := fixtureFS()
	idxSmall, err := help.LoadTopics(small)
	if err != nil {
		t.Fatalf("LoadTopics(small): %v", err)
	}
	big := fixtureFS()
	big["en/backups.md"] = &fstest.MapFile{Data: mdTopic("backups", "Backups", "Setup", 40,
		[]string{"snapshot"}, "Snapshots of all your shop data.\n")}
	idxBig, err := help.LoadTopics(big)
	if err != nil {
		t.Fatalf("LoadTopics(big): %v", err)
	}
	if got, want := len(idxSmall.ByID["en"]), 3; got != want {
		t.Fatalf("small index en topics = %d, want %d", got, want)
	}
	if got, want := len(idxBig.ByID["en"]), 4; got != want {
		t.Fatalf("big index en topics = %d, want %d", got, want)
	}
	if idxBig.ByID["en"]["backups"] == nil {
		t.Fatalf("added file did not become a topic")
	}
}
