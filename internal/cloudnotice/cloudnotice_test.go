package cloudnotice

import (
	"strings"
	"testing"
	"time"
)

func validItem() Item {
	now := time.Now().UTC()
	return Item{
		ID: "11111111-1111-1111-1111-111111111111", Rev: 1,
		Sender:   Sender{Kind: "platform", Name: "Universal Till"},
		Severity: "important",
		Texts: map[string]Text{
			"en-gb": {Title: "Update before Friday", Body: "Install **1.4.2** now."},
		},
		StartsAt:  now.Add(-time.Hour),
		ExpiresAt: now.Add(24 * time.Hour),
		CreatedAt: now.Add(-time.Hour),
	}
}

func TestValidate_AcceptsAGoodItem(t *testing.T) {
	if err := Validate(validItem(), time.Now()); err != nil {
		t.Fatalf("good item refused: %v", err)
	}
}

func TestValidate_RefusesBadItems(t *testing.T) {
	cases := map[string]func(*Item){
		"bad id":              func(i *Item) { i.ID = "x y" },
		"empty id":            func(i *Item) { i.ID = "" },
		"bad severity":        func(i *Item) { i.Severity = "urgent" },
		"bad sender kind":     func(i *Item) { i.Sender.Kind = "root" },
		"long sender name":    func(i *Item) { i.Sender.Name = strings.Repeat("a", 81) },
		"empty sender name":   func(i *Item) { i.Sender.Name = "" },
		"no en-gb":            func(i *Item) { i.Texts = map[string]Text{"de-de": {Title: "a", Body: "b"}} },
		"long title":          func(i *Item) { i.Texts["en-gb"] = Text{Title: strings.Repeat("t", 81), Body: "b"} },
		"long body":           func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: strings.Repeat("b", 1001)} },
		"empty title":         func(i *Item) { i.Texts["en-gb"] = Text{Title: "", Body: "b"} },
		"html in body":        func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "hi <img src=x onerror=1>"} },
		"image markdown":      func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "![x](https://a.example/x.png)"} },
		"js link in body":     func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "[go](javascript:alert(1))"} },
		"http link in body":   func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "[go](http://a.example)"} },
		"creds link in body":  func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "[go](https://u:p@a.example/)"} },
		"ends before starts":  func(i *Item) { i.ExpiresAt = i.StartsAt.Add(-time.Minute) },
		"over 90 days":        func(i *Item) { i.ExpiresAt = i.StartsAt.Add(91 * 24 * time.Hour) },
		"partner critical":    func(i *Item) { i.Sender.Kind = "partner"; i.Sender.Name = "Acme"; i.Severity = "critical" },
		"link http":           func(i *Item) { i.Link = &Link{Kind: "https", Target: "http://a.example", Label: "x"} },
		"link javascript":     func(i *Item) { i.Link = &Link{Kind: "https", Target: "javascript:alert(1)"} },
		"link data":           func(i *Item) { i.Link = &Link{Kind: "https", Target: "data:text/html,x"} },
		"link creds":          func(i *Item) { i.Link = &Link{Kind: "https", Target: "https://u:p@a.example"} },
		"link unknown route":  func(i *Item) { i.Link = &Link{Kind: "route", Target: "/api/danger"} },
		"link route external": func(i *Item) { i.Link = &Link{Kind: "route", Target: "//evil.example"} },
		"link unknown kind":   func(i *Item) { i.Link = &Link{Kind: "tel", Target: "123"} },
		"partner link": func(i *Item) {
			i.Sender.Kind = "partner"
			i.Sender.Name = "Acme"
			i.Link = &Link{Kind: "https", Target: "https://a.example"}
		},
	}
	for name, mut := range cases {
		it := validItem()
		mut(&it)
		if err := Validate(it, time.Now()); err == nil {
			t.Errorf("%s: accepted, want refused", name)
		}
	}
}

func TestValidate_AcceptsAllowedLinks(t *testing.T) {
	for _, l := range []Link{
		{Kind: "route", Target: "/settings", Label: "Open settings"},
		{Kind: "https", Target: "https://universaltill.com/news", Label: "Read"},
	} {
		it := validItem()
		l := l
		it.Link = &l
		if err := Validate(it, time.Now()); err != nil {
			t.Errorf("%+v refused: %v", l, err)
		}
	}
}

func TestPickText_Fallbacks(t *testing.T) {
	texts := map[string]Text{"en-gb": {Title: "E", Body: "e"}, "de-de": {Title: "D", Body: "d"}}
	for lang, want := range map[string]string{"de-de": "D", "de": "D", "DE": "D", "tr": "E", "": "E", "fa-ir": "E"} {
		_, got, ok := PickText(texts, lang)
		if !ok || got.Title != want {
			t.Errorf("PickText(%q) = %q ok=%v, want %q", lang, got.Title, ok, want)
		}
	}
	if _, _, ok := PickText(map[string]Text{"de-de": {Title: "D"}}, "tr"); !ok {
		t.Error("an item with no en-gb falls back to any text")
	}
	if _, _, ok := PickText(nil, "en"); ok {
		t.Error("no texts must report !ok")
	}
}

func TestHave_StableAndOrderIndependent(t *testing.T) {
	if Have(nil) != "" {
		t.Fatal("empty set has an empty hash")
	}
	a := Have([]Pair{{"b", 2}, {"a", 1}})
	b := Have([]Pair{{"a", 1}, {"b", 2}})
	if a == "" || a != b {
		t.Fatalf("order changed the hash: %q vs %q", a, b)
	}
	if Have([]Pair{{"a", 1}, {"b", 3}}) == a {
		t.Fatal("rev change did not change the hash")
	}
}

func TestRenderBody_SafeSubset(t *testing.T) {
	got := string(RenderBody("a **bold** and *it*\nline2 [site](https://universaltill.com/x?a=1&b=2)"))
	for _, want := range []string{"<strong>bold</strong>", "<em>it</em>", "<br>",
		`<a href="https://universaltill.com/x?a=1&amp;b=2" target="_blank" rel="noopener noreferrer">site</a>`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestRenderBody_EscapesEverythingElse(t *testing.T) {
	for _, in := range []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[x](javascript:alert(1))`,
		`[x](data:text/html,hi)`,
		`[x](http://insecure.example)`,
		`"><svg onload=1>`,
	} {
		got := string(RenderBody(in))
		if strings.Contains(got, "<script") || strings.Contains(got, "<img") || strings.Contains(got, "<svg") {
			t.Errorf("%q rendered live markup: %q", in, got)
		}
		if strings.Contains(got, `href="javascript`) || strings.Contains(got, `href="data`) || strings.Contains(got, `href="http:`) {
			t.Errorf("%q rendered an unsafe href: %q", in, got)
		}
	}
}

func TestRenderBody_RTLAndLongUnicode(t *testing.T) {
	got := string(RenderBody("پیام **مهم** " + strings.Repeat("ä", 500)))
	if !strings.Contains(got, "<strong>مهم</strong>") {
		t.Errorf("RTL bold lost: %q", got)
	}
}

func TestParseLink_OnlySafeLinksSurviveTheStore(t *testing.T) {
	if l := ParseLink(`{"kind":"route","target":"/settings"}`); l == nil || l.Target != "/settings" {
		t.Fatalf("route link = %v", l)
	}
	if l := ParseLink(`{"kind":"https","target":"https://universaltill.com/x","label":"More"}`); l == nil {
		t.Fatal("https link to the allow-listed host must survive")
	}
	for _, bad := range []string{"", "{", `{"kind":"https","target":"http://universaltill.com/x"}`,
		`{"kind":"https","target":"javascript:alert(1)"}`, `{"kind":"route","target":"//evil.example"}`,
		`{"kind":"https","target":"https://evil.example/"}`, `{"kind":"https","target":"https://u:p@universaltill.com/"}`} {
		if l := ParseLink(bad); l != nil {
			t.Errorf("ParseLink(%q) = %v, want nil", bad, l)
		}
	}
}
