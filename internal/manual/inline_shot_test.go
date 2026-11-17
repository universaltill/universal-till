package manual

import (
	"strings"
	"testing"
	"testing/fstest"
)

// Inline extra screenshots (ut-docs#2615): a topic's markdown references
// `![alt](img/<id>-<name>.png)`, locale-neutral; Load resolves it per locale
// with an en fallback, the same rule as the header figure.

func shotTopic(id, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nid: " + id + "\ntitle: T\nroutes: [/t]\n---\n\n# T\n\n" + body)}
}

const inlineBody = "- Layout A\n  ![Alt A](img/t-a.png)\n- Layout B\n"

func TestInlineShot_PerLocaleWithEnFallback(t *testing.T) {
	fsys := fstest.MapFS{
		"help/en/t.md":        shotTopic("t", inlineBody),
		"help/tr/t.md":        shotTopic("t", inlineBody),
		"help/fa/t.md":        shotTopic("t", inlineBody),
		"help/img/en/t-a.png": tinyPNG,
		"help/img/tr/t-a.png": tinyPNG,
	}
	lib, err := Load(fsys, "help")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for locale, want := range map[string]string{"en": "en", "tr": "tr", "fa": "en", "de": "en"} {
		tp, ok := lib.Topic(locale, "t")
		if !ok {
			t.Fatalf("%s/t not found", locale)
		}
		html := string(tp.HTML)
		src := `src="/help/img/` + want + `/t-a.png"`
		if !strings.Contains(html, src) {
			t.Errorf("%s: want %s in %s", locale, src, html)
		}
		if !strings.Contains(html, `alt="Alt A"`) || !strings.Contains(html, `loading="lazy"`) {
			t.Errorf("%s: alt/loading missing: %s", locale, html)
		}
		li := html[strings.Index(html, "<li>"):strings.Index(html, "</li>")]
		if !strings.Contains(li, "<img") {
			t.Errorf("%s: image should sit inside the first <li>: %s", locale, html)
		}
		if strings.Contains(html, "img/t-a.png\"") && !strings.Contains(html, "/help/img/") {
			t.Errorf("%s: neutral reference leaked: %s", locale, html)
		}
	}
}

func TestInlineShot_MissingEverywhere(t *testing.T) {
	fsys := fstest.MapFS{"help/en/t.md": shotTopic("t", inlineBody)}
	lib, err := Load(fsys, "help")
	if err != nil {
		t.Fatalf("a missing screenshot must not fail Load: %v", err)
	}
	tp, _ := lib.Topic("en", "t")
	if strings.Contains(string(tp.HTML), "<img") || strings.Contains(string(tp.HTML), "t-a") {
		t.Errorf("unresolved image should be dropped, got %s", tp.HTML)
	}
	if got := lib.MissingImages(); len(got) != 1 || got[0] != "en/t: img/t-a.png" {
		t.Errorf("MissingImages = %v", got)
	}
}

func TestInlineShot_RejectsBadReferences(t *testing.T) {
	for _, dst := range []string{
		"/help/img/en/t-a.png",
		"https://example.com/t-a.png",
		"../img/t-a.png",
		"img/en/t-a.png",
		"img/other-a.png", // another topic's stem
		"img/t.png",       // the header figure itself
		"img/T-a.png",
		"img/t-a.jpg",
	} {
		fsys := fstest.MapFS{"help/en/t.md": shotTopic("t", "![x]("+dst+")\n")}
		_, err := Load(fsys, "help")
		if err == nil {
			t.Errorf("%q should be refused", dst)
			continue
		}
		if !strings.Contains(err.Error(), dst) || !strings.Contains(err.Error(), "img/<id>-<name>.png") {
			t.Errorf("%q: error should name the destination and the rule, got %v", dst, err)
		}
	}
}

// A malformed reference in a non-English topic (e.g. a Weblate edit that
// localises the path) must not fail Load - that would nil Builtin() and break
// every "?" link. It is dropped and reported via MissingImages, which
// checkhelptopics fails on. English stays a hard error.
func TestInlineShot_MalformedRefInTranslationIsDroppedAndReported(t *testing.T) {
	fsys := fstest.MapFS{
		"help/en/t.md": shotTopic("t", inlineBody),
		"help/fa/t.md": shotTopic("t", "- Layout A\n  ![Alt](img/t-Kutu.png)\n"),
	}
	lib, err := Load(fsys, "help")
	if err != nil {
		t.Fatalf("a bad ref in fa must not fail Load: %v", err)
	}
	tp, ok := lib.Topic("fa", "t")
	if !ok {
		t.Fatal("fa/t not loaded")
	}
	if strings.Contains(string(tp.HTML), "<img") || strings.Contains(string(tp.HTML), "Kutu") {
		t.Errorf("malformed image should be dropped, got %s", tp.HTML)
	}
	found := false
	for _, m := range lib.MissingImages() {
		if strings.Contains(m, "fa/t") && strings.Contains(m, "t-Kutu.png") {
			found = true
		}
	}
	if !found {
		t.Errorf("MissingImages should report fa/t with the ref, got %v", lib.MissingImages())
	}
	if _, err := Load(fstest.MapFS{"help/en/t.md": shotTopic("t", "![x](img/t-Kutu.png)\n")}, "help"); err == nil {
		t.Error("a malformed ref in the en topic must still fail Load")
	}
}

func TestInlineShot_NoPurchaseRenderUsesResolver(t *testing.T) {
	body := "<!-- plan-purchase -->\nBuy.\n<!-- /plan-purchase -->\n\n" + inlineBody
	fsys := fstest.MapFS{"help/en/t.md": shotTopic("t", body), "help/img/en/t-a.png": tinyPNG}
	lib, err := Load(fsys, "help")
	if err != nil {
		t.Fatal(err)
	}
	tp, _ := lib.Topic("en", "t")
	if !strings.Contains(string(tp.WithoutPlanPurchase().HTML), `/help/img/en/t-a.png`) {
		t.Errorf("no-purchase render lost the image: %s", tp.WithoutPlanPurchase().HTML)
	}
}

func TestPlainText_StripsInlineImages(t *testing.T) {
	got := plainText("- Layout A ![Alt A](img/t-a.png) tail\n  ![Alt B](img/t-b.png)\nplain\n")
	for _, bad := range []string{"![", "img/", ".png", "Alt A", "Alt B"} {
		if strings.Contains(got, bad) {
			t.Errorf("plainText leaked %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "Layout A") || !strings.Contains(got, "tail") || !strings.Contains(got, "plain") {
		t.Errorf("prose lost: %q", got)
	}
}

func TestSearchSnippet_NoImageMarkup(t *testing.T) {
	fsys := fstest.MapFS{
		"help/en/t.md": shotTopic("t", "- Zebra layout ![Alt A](img/t-a.png)\n  ![Alt B](img/t-b.png)\n"),
	}
	lib, err := Load(fsys, "help")
	if err != nil {
		t.Fatal(err)
	}
	res := lib.SearchFor("en", "zebra", true)
	if len(res) == 0 {
		t.Fatal("no hit")
	}
	for _, bad := range []string{"![", ".png", "img/"} {
		if strings.Contains(res[0].Snippet, bad) {
			t.Errorf("snippet leaked %q: %q", bad, res[0].Snippet)
		}
	}
}

// Real-tree truth: the embedded sell topic carries its three layout shots and
// none is missing from web/help/img.
func TestBuiltin_SellBrowseShots(t *testing.T) {
	lib := Builtin()
	if lib == nil {
		t.Fatal("embedded manual unavailable")
	}
	if got := lib.MissingImages(); len(got) != 0 {
		t.Errorf("embedded manual references screenshots that are not committed: %v", got)
	}
	tp, ok := lib.Topic("en", "sell")
	if !ok {
		t.Fatal("en/sell missing")
	}
	for _, stem := range []string{"sell-browse-tiles", "sell-browse-filters", "sell-browse-strip"} {
		if !strings.Contains(string(tp.HTML), `src="/help/img/en/`+stem+`.png"`) {
			t.Errorf("en/sell lacks %s", stem)
		}
	}
}
