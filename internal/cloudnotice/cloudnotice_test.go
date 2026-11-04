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
		// Invisible formatting characters (Cf/Zl/Zp) and controls: a bidi
		// override reverses what the operator reads, a zero-width space
		// splits a title. Mirrors the cloud's rule; the till checks itself.
		"bidi override in title": func(i *Item) { i.Texts["en-gb"] = Text{Title: "Update ‮gnp.exe", Body: "b"} },
		"bidi isolate in body":   func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "see ⁧evil⁩"} },
		"zero-width space":       func(i *Item) { i.Texts["en-gb"] = Text{Title: "Up​date", Body: "b"} },
		"line separator":         func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "a b"} },
		"soft hyphen":            func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "a­b"} },
		"control in body":        func(i *Item) { i.Texts["en-gb"] = Text{Title: "t", Body: "a\x07b"} },
		"newline in title":       func(i *Item) { i.Texts["en-gb"] = Text{Title: "a\nb", Body: "b"} },
		"bidi in sender name":    func(i *Item) { i.Sender.Name = "Universal ‮Till" },
		"bidi in link label":     func(i *Item) { i.Link = &Link{Kind: "route", Target: "/settings", Label: "Open‮"} },
		"starts 31 days ahead": func(i *Item) {
			i.StartsAt = time.Now().Add(31 * 24 * time.Hour)
			i.ExpiresAt = i.StartsAt.Add(time.Hour)
		},
		"bidi in non-en language": func(i *Item) { i.Texts["de-de"] = Text{Title: "Titel ‮", Body: "b"} },
	}
	for name, mut := range cases {
		it := validItem()
		mut(&it)
		if err := Validate(it, time.Now()); err == nil {
			t.Errorf("%s: accepted, want refused", name)
		}
	}
}

func TestValidate_KeepsTheJoinersAndMarksRealTextNeeds(t *testing.T) {
	// ZWNJ (Persian), ZWJ (emoji sequences), LRM/RLM/ALM (mixed direction)
	// and a newline in the body are ordinary text, never refused.
	it := validItem()
	it.Texts["fa-ir"] = Text{Title: "می‌خواهم ‏", Body: "خط اول\nخط دوم ؜ 👨‍👩‍👧 ‎"}
	it.Texts["en-gb"] = Text{Title: "Plain", Body: "line one\nline two"}
	if err := Validate(it, time.Now()); err != nil {
		t.Fatalf("real text refused: %v", err)
	}
}

func TestSortForCap_CriticalFirstThenNewest(t *testing.T) {
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	items := []Item{
		{ID: "i-old", Severity: Info, CreatedAt: base.Add(-2 * time.Hour)},
		{ID: "i-new", Severity: Info, CreatedAt: base},
		{ID: "imp", Severity: Important, CreatedAt: base.Add(-3 * time.Hour)},
		{ID: "crit", Severity: Critical, CreatedAt: base.Add(-9 * time.Hour)},
		{ID: "weird", Severity: "zzz", CreatedAt: base},
	}
	SortForCap(items)
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = it.ID
	}
	want := "crit imp i-new i-old weird"
	if strings.Join(got, " ") != want {
		t.Fatalf("order = %q, want %q", strings.Join(got, " "), want)
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
		`{"kind":"https","target":"https://evil.example/"}`, `{"kind":"https","target":"https://u:p@universaltill.com/"}`, // mail-address:allow URL credentials test input, not a mail address
	} {
		if l := ParseLink(bad); l != nil {
			t.Errorf("ParseLink(%q) = %v, want nil", bad, l)
		}
	}
}

// ADR-0176 §6: an invalid link drops the LINK, never the notice (a critical
// notice must still reach the shop). Clean is what cloudsync applies.
func TestClean_InvalidLinkDropsOnlyTheLink(t *testing.T) {
	long := "https://universaltill.com/" + strings.Repeat("a", 280) // 306 bytes, over the till's 300
	cases := map[string]Link{
		"route off the allow-list": {Kind: "route", Target: "/admin/secret", Label: "x"},
		"javascript scheme":        {Kind: "https", Target: "javascript:alert(1)"},
		"foreign host":             {Kind: "https", Target: "https://evil.example/x"},
		"over-long target":         {Kind: "https", Target: long},
		"unknown kind":             {Kind: "mailto", Target: "a@b.c"},
		"bidi label":               {Kind: "route", Target: "/settings", Label: "Open‮"},
	}
	for _, sev := range []string{Info, Important, Critical} {
		for name, l := range cases {
			l := l
			it := validItem()
			it.Severity = sev
			it.Link = &l
			got, err := Clean(it, time.Now())
			if err != nil {
				t.Errorf("%s/%s: notice dropped (%v), want kept without its link", sev, name, err)
				continue
			}
			if got.Link != nil {
				t.Errorf("%s/%s: link kept: %+v", sev, name, got.Link)
			}
			if got.ID != it.ID || got.Texts["en-gb"].Title != it.Texts["en-gb"].Title {
				t.Errorf("%s/%s: notice content changed", sev, name)
			}
		}
	}
}

func TestClean_GoodLinkIsKept_AndOtherDefectsStillDropTheNotice(t *testing.T) {
	it := validItem()
	it.Link = &Link{Kind: "route", Target: "/settings", Label: "Open settings"}
	got, err := Clean(it, time.Now())
	if err != nil || got.Link == nil || got.Link.Target != "/settings" {
		t.Fatalf("good link: %+v err=%v", got.Link, err)
	}
	bad := validItem()
	bad.Texts["en-gb"] = Text{Title: "<script>x</script>", Body: "b"}
	bad.Link = &Link{Kind: "route", Target: "/settings"}
	if _, err := Clean(bad, time.Now()); err == nil {
		t.Fatal("a notice with unsafe text must still be dropped")
	}
}

func TestClean_PartnerLinkIsStrippedNotFatal(t *testing.T) {
	it := validItem()
	it.Sender = Sender{Kind: SenderPartner, Name: "Acme"}
	it.Severity = Important
	it.Link = &Link{Kind: "route", Target: "/settings"}
	got, err := Clean(it, time.Now())
	if err != nil || got.Link != nil {
		t.Fatalf("partner link: link=%v err=%v, want kept notice without link", got.Link, err)
	}
}

func TestClean_DoesNotMutateTheInput(t *testing.T) {
	it := validItem()
	it.Link = &Link{Kind: "https", Target: "https://evil.example/"}
	if _, err := Clean(it, time.Now()); err != nil {
		t.Fatal(err)
	}
	if it.Link == nil {
		t.Fatal("Clean changed its argument")
	}
}
