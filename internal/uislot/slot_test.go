package uislot

import (
	"strings"
	"testing"
)

// ADR-0088 Decision E: legal, fiscal and safety surfaces are not amendable-
// away by third-party code. One subtest per protected key, so a key dropped
// from ProtectedMenuKeys fails by name.
func TestParseMenuAmendments_ProtectedKeyCannotBeHidden(t *testing.T) {
	// ut-docs#2008 added "/admin": it's now the ONLY launcher path to both
	// statutory fiscal pages (registerMenu drops them from the flat /menu
	// grid unconditionally), so hiding it would make both unreachable from
	// any UI surface -- exactly what Decision E exists to prevent.
	if len(ProtectedMenuKeys) != 8 {
		t.Fatalf("ADR-0088 names 8 protected keys, got %d: %v", len(ProtectedMenuKeys), ProtectedMenuKeys)
	}
	for _, key := range ProtectedMenuKeys {
		t.Run(key, func(t *testing.T) {
			_, err := ParseMenuAmendments("com.example.layout", map[string]any{
				"amendments": []any{map[string]any{"key": key, "hide": true}},
			})
			if err == nil {
				t.Fatalf("hide of protected key %q must be refused", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("refusal must name the key %q, got: %v", key, err)
			}
		})
	}
}

// Decision E's second half: reorder / re-label / re-group / re-icon ARE
// permitted on a protected key — they keep the destination visible.
// A protected key may be MOVED and GROUPED — that positions a statutory
// destination without disguising it, and a vertical legitimately needs to.
func TestParseMenuAmendments_ProtectedKeyCanBeReorderedAndRegrouped(t *testing.T) {
	for _, key := range ProtectedMenuKeys {
		t.Run(key, func(t *testing.T) {
			got, err := ParseMenuAmendments("com.example.layout", map[string]any{
				"amendments": []any{map[string]any{
					"key": key, "order": float64(5), "group": "g.k",
				}},
			})
			if err != nil {
				t.Fatalf("reordering/regrouping protected key %q must be allowed: %v", key, err)
			}
			if len(got) != 1 || got[0].Key != key || got[0].Order == nil || *got[0].Order != 5 ||
				got[0].Group != "g.k" || got[0].Hide {
				t.Fatalf("parsed amendment mismatch: %+v", got)
			}
			if got[0].PluginID != "com.example.layout" {
				t.Fatalf("amendment must carry its plugin id, got %+v", got[0])
			}
		})
	}
}

// ...but it may NOT be re-labelled or re-iconed. Refusing only `hide` left
// a hole the independent review of ut-docs#1904 drove end to end: a plugin
// re-labelled /report-issue to "Catalog" with a tag icon and moved it last,
// and the "Hidden menu tiles" recovery page reported nothing amended,
// because it lists hides. A tile whose label and icon are attacker-chosen
// is not meaningfully "visible" — which is all Decision E's permission to
// restructure protected keys was ever resting on. The label and the icon
// ARE the identity a merchant recognises a statutory surface by, so they
// are protected exactly like its presence.
func TestParseMenuAmendments_ProtectedKeyCannotBeRelabelledOrReiconed(t *testing.T) {
	for _, key := range ProtectedMenuKeys {
		for _, tc := range []struct{ name, field, want string }{
			{"relabel", "label_key", "cannot be re-labelled"},
			{"reicon", "icon", "cannot be re-iconed"},
		} {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				_, err := ParseMenuAmendments("com.example.layout", map[string]any{
					"amendments": []any{map[string]any{"key": key, tc.field: "nav.catalog"}},
				})
				if err == nil {
					t.Fatalf("%s of protected key %q must be refused at install", tc.name, key)
				}
				if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), key) {
					t.Fatalf("error must name the key and what was refused, got: %v", err)
				}
			})
		}
	}
}

// A non-protected key stays fully amendable — the refusal above must not
// have quietly frozen the mechanism's whole point.
func TestParseMenuAmendments_UnprotectedKeyStillFullyAmendable(t *testing.T) {
	got, err := ParseMenuAmendments("com.example.layout", map[string]any{
		"amendments": []any{map[string]any{
			"key": "/items", "order": float64(50), "label_key": "layout.salon.services", "icon": "scissors",
		}},
	})
	if err != nil {
		t.Fatalf("/items is not protected and must stay amendable: %v", err)
	}
	if len(got) != 1 || got[0].LabelKey != "layout.salon.services" || got[0].Icon != "scissors" {
		t.Fatalf("parsed amendment mismatch: %+v", got)
	}
}

func TestParseMenuAmendments_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]any
		want   string // substring the error must carry
	}{
		{"unknown key", map[string]any{"amendments": []any{map[string]any{"key": "/no-such-page", "hide": true}}}, "/no-such-page"},
		{"unsupported slot", map[string]any{"slot": "sale-rail", "amendments": []any{map[string]any{"key": "/tables", "hide": true}}}, "sale-rail"},
		{"missing key", map[string]any{"amendments": []any{map[string]any{"hide": true}}}, "key"},
		{"unknown field is a typo, not a no-op", map[string]any{"amendments": []any{map[string]any{"key": "/tables", "lable_key": "x"}}}, "lable_key"},
		{"amendment that does nothing", map[string]any{"amendments": []any{map[string]any{"key": "/tables"}}}, "/tables"},
		{"hide combined with a restructure", map[string]any{"amendments": []any{map[string]any{"key": "/tables", "hide": true, "order": float64(1)}}}, "hide"},
		{"same key twice in one document", map[string]any{"amendments": []any{
			map[string]any{"key": "/tables", "hide": true},
			map[string]any{"key": "/tables", "order": float64(1)},
		}}, "/tables"},
		{"amendments not a list", map[string]any{"amendments": "nope"}, "amendments"},
		{"config without amendments", map[string]any{"slot": "menu"}, "amendments"},
		{"non-integer order", map[string]any{"amendments": []any{map[string]any{"key": "/tables", "order": 1.5}}}, "order"},
		{"non-string label_key", map[string]any{"amendments": []any{map[string]any{"key": "/tables", "label_key": float64(3)}}}, "label_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseMenuAmendments("com.example.layout", tc.config)
			if err == nil {
				t.Fatalf("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// A layout entry with no config at all is the taxonomy test's minimal shape
// ({"type":"layout","key":"k","label":"L"}) — it declares nothing and must
// parse as zero amendments, not as an error.
func TestParseMenuAmendments_EmptyConfigIsZeroAmendments(t *testing.T) {
	for _, cfg := range []map[string]any{nil, {}} {
		got, err := ParseMenuAmendments("p", cfg)
		if err != nil || len(got) != 0 {
			t.Fatalf("empty config: got %v, %v", got, err)
		}
	}
	got, err := ParseAmendmentsJSON("p", "")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty config_json: got %v, %v", got, err)
	}
}

// A Config map assembled in Go carries an int where JSON decoding carries a
// float64 — both are the same declaration.
func TestParseMenuAmendments_AcceptsGoIntOrder(t *testing.T) {
	got, err := ParseMenuAmendments("p", map[string]any{"amendments": []any{map[string]any{"key": "/items", "order": 10}}})
	if err != nil || len(got) != 1 || got[0].Order == nil || *got[0].Order != 10 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseAmendmentsJSON_RoundTripsPersistedConfig(t *testing.T) {
	got, err := ParseAmendmentsJSON("p", `{"slot":"menu","amendments":[{"key":"/tables","hide":true},{"key":"/items","label_key":"a.b","order":50}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Hide || got[0].Key != "/tables" || got[1].LabelKey != "a.b" || *got[1].Order != 50 {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

// Decision F: hide is idempotent across plugins; reorder/re-label/re-group/
// re-icon of the same key by two plugins is a conflict naming the incumbent.
func TestFindConflict(t *testing.T) {
	one := 1
	hideA := Amendment{PluginID: "com.a", Key: "/tables", Hide: true}
	hideB := Amendment{PluginID: "com.b", Key: "/tables", Hide: true}
	labelA := Amendment{PluginID: "com.a", Key: "/items", LabelKey: "a.services"}
	labelB := Amendment{PluginID: "com.b", Key: "/items", LabelKey: "b.services"}
	orderB := Amendment{PluginID: "com.b", Key: "/items", Order: &one}
	groupB := Amendment{PluginID: "com.b", Key: "/items", Group: "g"}
	iconB := Amendment{PluginID: "com.b", Key: "/items", Icon: "tag"}

	if c, ok := FindConflict([]Amendment{hideB}, []Amendment{hideA}); ok {
		t.Fatalf("two hides of the same key must not conflict, got %+v", c)
	}
	for _, cand := range []Amendment{labelB, orderB, groupB, iconB} {
		c, ok := FindConflict([]Amendment{cand}, []Amendment{labelA})
		if !ok {
			t.Fatalf("%+v against an incumbent restructure of the same key must conflict", cand)
		}
		if c.Key != "/items" || c.Incumbent != "com.a" {
			t.Fatalf("conflict must name the key and incumbent, got %+v", c)
		}
	}
	if _, ok := FindConflict([]Amendment{labelB}, []Amendment{hideA, {PluginID: "com.a", Key: "/orders", Order: &one}}); ok {
		t.Fatal("a restructure of a different key, or a hide, is not a conflict")
	}
	// A plugin never conflicts with its own rows (reinstall / upgrade).
	if _, ok := FindConflict([]Amendment{labelA}, []Amendment{labelA}); ok {
		t.Fatal("a plugin must not conflict with itself")
	}
}

func sampleEntries() []Entry {
	return []Entry{
		{Key: "/a", Href: "/a", LabelKey: "a.title", Icon: "tag", Order: 100},
		{Key: "/b", Href: "/b", LabelKey: "b.title", Icon: "bell", Order: 200},
		{Key: "/c", Href: "/c", LabelKey: "c.title", Icon: "", Order: 300},
	}
}

// Decision I: with no amendments the resolver hands back the caller's own
// slice — no copy, no sort, no allocation.
func TestResolve_ZeroAmendmentsReturnsInputUnchanged(t *testing.T) {
	in := sampleEntries()
	out := Resolve(in, nil)
	if len(out) != len(in) || &out[0] != &in[0] {
		t.Fatalf("zero amendments must return the input slice itself")
	}
	out = Resolve(in, []Amendment{})
	if len(out) != len(in) || &out[0] != &in[0] {
		t.Fatalf("an empty (non-nil) amendment list is still the zero path")
	}
}

// The falsifiable half of Decision I, asserted in CI (a benchmark alone
// only reports; it is never failed by `go test`).
func TestResolve_ZeroAmendmentsAllocatesNothing(t *testing.T) {
	in := CoreMenu
	var sink []Entry
	allocs := testing.AllocsPerRun(1000, func() { sink = Resolve(in, nil) })
	if allocs != 0 {
		t.Fatalf("zero-amendment Resolve allocated %v times per run, want 0 (ADR-0088 Decision I)", allocs)
	}
	_ = sink
}

func BenchmarkResolve_ZeroAmendments(b *testing.B) {
	in := CoreMenu
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Resolve(in, nil)
	}
}

func TestResolve_HideRemovesOnlyThatEntry(t *testing.T) {
	in := sampleEntries()
	out := Resolve(in, []Amendment{{PluginID: "p", Key: "/b", Hide: true}})
	if len(out) != 2 || out[0].Key != "/a" || out[1].Key != "/c" {
		t.Fatalf("got %+v", out)
	}
	if len(in) != 3 {
		t.Fatal("Resolve must not mutate its input")
	}
}

func TestResolve_ReorderIsStableAcrossEqualOrders(t *testing.T) {
	in := sampleEntries()
	ten := 10
	// /c moves ahead of /a and /b; /a and /b keep their relative order.
	out := Resolve(in, []Amendment{{PluginID: "p", Key: "/c", Order: &ten}})
	if out[0].Key != "/c" || out[1].Key != "/a" || out[2].Key != "/b" {
		t.Fatalf("got %+v", out)
	}
	// An equal order value keeps declaration order (stable sort).
	twoHundred := 200
	out = Resolve(in, []Amendment{{PluginID: "p", Key: "/a", Order: &twoHundred}})
	if out[0].Key != "/a" || out[1].Key != "/b" {
		t.Fatalf("equal orders must keep declaration order, got %+v", out)
	}
	if in[0].Order != 100 {
		t.Fatal("Resolve must not mutate its input")
	}
}

// Decisions G and H: a re-label / re-icon keeps the core value beside it so
// the renderer can fall back to it.
func TestResolve_RelabelAndReiconKeepCoreFallbacks(t *testing.T) {
	out := Resolve(sampleEntries(), []Amendment{{PluginID: "p", Key: "/a", LabelKey: "p.services", Icon: "scissors", Group: "p.group"}})
	a := out[0]
	if a.LabelKey != "p.services" || a.LabelFallback != "a.title" {
		t.Fatalf("label: got %+v", a)
	}
	if a.Icon != "scissors" || a.IconFallback != "tag" {
		t.Fatalf("icon: got %+v", a)
	}
	if a.Group != "p.group" {
		t.Fatalf("group: got %+v", a)
	}
	if out[1].LabelFallback != "" || out[1].IconFallback != "" {
		t.Fatalf("unamended entry must carry no fallback, got %+v", out[1])
	}
}

func TestCoreMenu_IsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	prev := -1
	// ut-docs#1959: with zero amendments Resolve() never re-sorts/re-groups
	// (Decision I, checked above), so groupTogether never runs on the
	// zero-plugin path — a Group value is only ever rendered correctly if
	// its members are already CONTIGUOUS in this declared slice. lastGroup
	// + closedGroups catch the failure mode directly: an entry reopening a
	// group after a different group (or an ungrouped entry) interrupted it
	// renders that group's heading a second time (menu_page.go's
	// GroupHeading logic keys off "did the group change since the last
	// tile", so a reopened group looks exactly like a brand new one).
	lastGroup := ""
	closedGroups := map[string]bool{}
	for _, e := range CoreMenu {
		if e.Key == "" || e.Key != e.Href {
			t.Errorf("core entry key must equal its href (the ADR names protected keys by path): %+v", e)
		}
		if seen[e.Key] {
			t.Errorf("duplicate core key %q", e.Key)
		}
		seen[e.Key] = true
		if e.LabelKey == "" || e.Icon == "" {
			t.Errorf("core entry %q needs a locale key and an icon name", e.Key)
		}
		if e.Order <= prev {
			t.Errorf("core table must be declared in ascending Order (zero-plugin path never sorts): %q has %d after %d", e.Key, e.Order, prev)
		}
		prev = e.Order
		if e.LabelFallback != "" || e.IconFallback != "" {
			t.Errorf("fallbacks are set by Resolve, never declared: %+v", e)
		}
		if e.Group != lastGroup {
			if lastGroup != "" {
				closedGroups[lastGroup] = true
			}
			if e.Group != "" && closedGroups[e.Group] {
				t.Errorf("group %q is not declared contiguously in CoreMenu: %q reopens it after another entry interrupted it — the zero-plugin render path would show its heading twice", e.Group, e.Key)
			}
		}
		lastGroup = e.Group
	}
	for _, p := range ProtectedMenuKeys {
		if !seen[p] {
			t.Errorf("protected key %q is not a declared core menu entry", p)
		}
		if !IsProtectedMenuKey(p) {
			t.Errorf("IsProtectedMenuKey(%q) = false", p)
		}
	}
	if IsProtectedMenuKey("/tables") {
		t.Error("/tables is not protected")
	}
	if _, ok := CoreMenuEntry("/tables"); !ok {
		t.Error("CoreMenuEntry must find a declared key")
	}
	if _, ok := CoreMenuEntry("/nope"); ok {
		t.Error("CoreMenuEntry must not find an undeclared key")
	}
}

// "re-group" must actually group. The renderer draws a heading whenever
// CONSECUTIVE entries change group, so before groupTogether two entries
// given the same group but landing apart in Order rendered the SAME
// heading twice with unrelated tiles between them (independent review of
// ut-docs#1904, F2) — run-labelling, not grouping.
func TestResolve_SameGroupEntriesEndUpAdjacent(t *testing.T) {
	entries := []Entry{
		{Key: "/a", Order: 10}, {Key: "/b", Order: 20}, {Key: "/c", Order: 30},
		{Key: "/d", Order: 40}, {Key: "/e", Order: 50},
	}
	five := 5
	got := Resolve(entries, []Amendment{
		{PluginID: "p", Key: "/a", Group: "g", Order: &five},
		{PluginID: "p", Key: "/d", Group: "g"},
	})
	var keys, groups []string
	for _, e := range got {
		keys = append(keys, e.Key)
		groups = append(groups, e.Group)
	}
	if len(got) != 5 {
		t.Fatalf("grouping must not drop or duplicate entries, got %v", keys)
	}
	// The two grouped entries are adjacent, so the renderer emits ONE heading.
	headings := 0
	prev := ""
	for _, g := range groups {
		if g != "" && g != prev {
			headings++
		}
		prev = g
	}
	if headings != 1 {
		t.Fatalf("same group must render one heading, got %d for %v / %v", headings, keys, groups)
	}
	if got[0].Key != "/a" || got[1].Key != "/d" {
		t.Fatalf("group members must gather at the earliest member's position, got %v", keys)
	}
}

// Grouping must be a no-op when no amendment sets a group — the zero and
// near-zero paths stay byte-identical.
func TestResolve_NoGroupAmendmentLeavesOrderUntouched(t *testing.T) {
	entries := []Entry{{Key: "/a", Order: 10}, {Key: "/b", Order: 20}, {Key: "/c", Order: 30}}
	got := Resolve(entries, []Amendment{{PluginID: "p", Key: "/b", LabelKey: "x.y"}})
	if len(got) != 3 || got[0].Key != "/a" || got[1].Key != "/b" || got[2].Key != "/c" {
		t.Fatalf("a label-only amendment must not reorder anything, got %+v", got)
	}
}

// ut-docs#1911: the Items slot's own core table, generalized from the
// same shape TestCoreMenu_IsWellFormed pins for the Menu slot — every key
// equals its href, is unique, declared in ascending Order, carries no
// pre-declared fallback, and every row also carries a SubtitleKey (the one
// field the Menu slot never uses).
func TestCoreItems_IsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	prev := -1
	for _, e := range CoreItems {
		if e.Key == "" || e.Key != e.Href {
			t.Errorf("core entry key must equal its href: %+v", e)
		}
		if seen[e.Key] {
			t.Errorf("duplicate core key %q", e.Key)
		}
		seen[e.Key] = true
		if e.LabelKey == "" || e.SubtitleKey == "" {
			t.Errorf("core Items entry %q needs a name and subtitle locale key", e.Key)
		}
		if e.Icon != "" {
			t.Errorf("Items slot rows carry no icon (items_rail.html draws none): %+v", e)
		}
		if e.Order <= prev {
			t.Errorf("core table must be declared in ascending Order (zero-plugin path never sorts): %q has %d after %d", e.Key, e.Order, prev)
		}
		prev = e.Order
		if e.LabelFallback != "" || e.IconFallback != "" {
			t.Errorf("fallbacks are set by Resolve, never declared: %+v", e)
		}
	}
	if len(ProtectedItemsKeys) != 0 {
		t.Fatalf("ADR-0088 names zero protected Items-slot keys today, got %v", ProtectedItemsKeys)
	}
	if IsProtectedItemsKey("/catalog") {
		t.Error("no Items-slot key is protected today")
	}
	if _, ok := CoreItemsEntry("/catalog"); !ok {
		t.Error("CoreItemsEntry must find a declared key")
	}
	if _, ok := CoreItemsEntry("/tables"); ok {
		t.Error("CoreItemsEntry must not find a Menu-slot key")
	}
}

// A layout plugin amending the Items slot follows the same schema and
// refusal shapes ParseMenuAmendments already has for order/label_key — this
// pins that the generalization (parseSlotAmendments) didn't silently break
// what the shipped plugins/layout-salon demo actually needs. (The Items
// slot's capability set is deliberately NARROWER than Menu's — see
// TestParseItemsAmendments_RefusesHideIconGroup below.)
func TestParseItemsAmendments_RelabelAndReorder(t *testing.T) {
	got, err := ParseItemsAmendments("com.example.layout", map[string]any{
		"slot": "items",
		"amendments": []any{
			map[string]any{"key": "/catalog", "label_key": "layout.salon.services", "order": float64(1)},
		},
	})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if len(got) != 1 || got[0].Slot != ItemsSlot || got[0].Key != "/catalog" || got[0].LabelKey != "layout.salon.services" || *got[0].Order != 1 {
		t.Fatalf("parsed amendment mismatch: %+v", got)
	}
}

// A Menu-slot key is not a declared Items-slot destination, and vice versa
// — the two core tables must not leak into each other's validation.
func TestParseItemsAmendments_RefusesAMenuOnlyKey(t *testing.T) {
	_, err := ParseItemsAmendments("com.example.layout", map[string]any{
		"amendments": []any{map[string]any{"key": "/tables", "hide": true}},
	})
	if err == nil || !strings.Contains(err.Error(), "/tables") {
		t.Fatalf("expected a refusal naming /tables, got %v", err)
	}
}

// ut-docs#1911 independent review, blockers 1+2 and finding 5: the Items
// slot has no findability/restore surface (Decision D) and its renderer
// draws no icon or group heading, so hide/icon/group must be refused at
// install — accepting-then-silently-ignoring is exactly the failure mode
// ADR-0088 exists to prevent, and hide specifically would let a plugin
// empty the whole rail (uislot.CoreItems has zero protected keys), which
// crashes the /items handler's sections[0] lookup.
func TestParseItemsAmendments_RefusesHideIconGroup(t *testing.T) {
	cases := []struct {
		name   string
		amend  map[string]any
		wantIn string
	}{
		{"hide", map[string]any{"key": "/catalog", "hide": true}, "hide"},
		{"icon", map[string]any{"key": "/catalog", "icon": "scissors"}, "icon"},
		{"group", map[string]any{"key": "/catalog", "group": "g.k"}, "group"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseItemsAmendments("com.example.layout", map[string]any{
				"amendments": []any{tc.amend},
			})
			if err == nil {
				t.Fatalf("%s must be refused on the items slot", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) || !strings.Contains(err.Error(), "/catalog") {
				t.Fatalf("error should name %q and the key, got: %v", tc.wantIn, err)
			}
		})
	}
}

// The narrower Items capability set must not have narrowed the Menu slot
// too — hide/icon/group all still work exactly as before this card.
func TestParseMenuAmendments_StillAllowsHideIconGroup(t *testing.T) {
	got, err := ParseMenuAmendments("com.example.layout", map[string]any{
		"amendments": []any{
			map[string]any{"key": "/tables", "hide": true},
			map[string]any{"key": "/items", "icon": "scissors", "group": "g.k"},
		},
	})
	if err != nil {
		t.Fatalf("Menu slot must still allow hide/icon/group: %v", err)
	}
	if len(got) != 2 || !got[0].Hide || got[1].Icon != "scissors" || got[1].Group != "g.k" {
		t.Fatalf("parsed amendments mismatch: %+v", got)
	}
}

// An empty-after-hide Items rail would crash items_page.go's sections[0] —
// this pins that the refusal above is what actually prevents it: hiding
// every declared Items-slot key one amendment at a time is refused on the
// FIRST one, not accepted until the rail is empty.
func TestParseItemsAmendments_CannotHideEvenASingleUnprotectedRow(t *testing.T) {
	if len(ProtectedItemsKeys) != 0 {
		t.Fatalf("this test's premise (zero protected Items keys) has changed — update it")
	}
	for _, e := range CoreItems {
		_, err := ParseItemsAmendments("p", map[string]any{
			"amendments": []any{map[string]any{"key": e.Key, "hide": true}},
		})
		if err == nil {
			t.Fatalf("hiding %q must be refused — the Items slot allows no hide at all", e.Key)
		}
	}
}

func TestParseAmendmentsJSON_DispatchesOnDeclaredSlot(t *testing.T) {
	menu, err := ParseAmendmentsJSON("p", `{"slot":"menu","amendments":[{"key":"/tables","hide":true}]}`)
	if err != nil || len(menu) != 1 || menu[0].Slot != MenuSlot {
		t.Fatalf("menu dispatch: got %+v, %v", menu, err)
	}
	items, err := ParseAmendmentsJSON("p", `{"slot":"items","amendments":[{"key":"/catalog","order":1}]}`)
	if err != nil || len(items) != 1 || items[0].Slot != ItemsSlot {
		t.Fatalf("items dispatch: got %+v, %v", items, err)
	}
	// No "slot" field at all — every manifest written before this card —
	// must still default to the Menu slot, unchanged.
	implicit, err := ParseAmendmentsJSON("p", `{"amendments":[{"key":"/tables","hide":true}]}`)
	if err != nil || len(implicit) != 1 || implicit[0].Slot != MenuSlot {
		t.Fatalf("no-slot default: got %+v, %v", implicit, err)
	}
	empty, err := ParseAmendmentsJSON("p", "")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty config_json: got %v, %v", empty, err)
	}
}

// Decision F's conflict check is scoped per slot (ut-docs#1911): two
// plugins restructuring the SAME STRING key in DIFFERENT slots must not
// conflict — only same-slot restructures of the same key do. Before the
// Slot field, FindConflict compared Key alone across the whole flat
// LayoutAmendments pool, which would have wrongly refused this pairing were
// a Menu key and an Items key ever to collide as strings.
func TestFindConflict_ScopedPerSlot(t *testing.T) {
	ten := 10
	menuAmendment := Amendment{PluginID: "com.a", Slot: MenuSlot, Key: "/shared", Order: &ten}
	itemsAmendment := Amendment{PluginID: "com.b", Slot: ItemsSlot, Key: "/shared", Order: &ten}
	if _, ok := FindConflict([]Amendment{itemsAmendment}, []Amendment{menuAmendment}); ok {
		t.Fatal("same key string in a DIFFERENT slot must not conflict")
	}
	sameSlotOther := Amendment{PluginID: "com.c", Slot: ItemsSlot, Key: "/shared", Order: &ten}
	if _, ok := FindConflict([]Amendment{itemsAmendment}, []Amendment{sameSlotOther}); !ok {
		t.Fatal("same key AND same slot from a different plugin must still conflict")
	}
}

// ut-docs#1912: the rail slot's own core table (web/ui/partials/nav.html's
// .nav-primary block, generalized the same way ut-docs#1911 generalized
// the /items rail) — every key equals its href, is unique, carries a label
// key and an icon (nav.html draws both), and is declared in ascending
// Order so the zero-plugin path never sorts. Pins the four entries by
// key/order because nav.html's DOM order IS this table's declared order
// (Decision I) — a reshuffle here silently reshuffles every page's rail.
func TestCoreRail_IsWellFormed(t *testing.T) {
	want := []struct {
		key, label, icon string
		order            int
	}{
		{"/", "nav.till", "shopping-cart", 100},
		{"/menu", "nav.menu", "menu", 200},
		{"/inventory", "kiosk.inventory", "package", 300},
		{"/orders", "nav.orders", "bell", 400},
	}
	if len(CoreRail) != len(want) {
		t.Fatalf("CoreRail has %d entries, want %d: %+v", len(CoreRail), len(want), CoreRail)
	}
	seen := map[string]bool{}
	prev := -1
	for i, e := range CoreRail {
		w := want[i]
		if e.Key != w.key || e.Href != w.key || e.LabelKey != w.label || e.Icon != w.icon || e.Order != w.order {
			t.Errorf("CoreRail[%d] = %+v, want key/href %q label %q icon %q order %d", i, e, w.key, w.label, w.icon, w.order)
		}
		if seen[e.Key] {
			t.Errorf("duplicate core key %q", e.Key)
		}
		seen[e.Key] = true
		if e.Order <= prev {
			t.Errorf("core table must be declared in ascending Order (zero-plugin path never sorts): %q has %d after %d", e.Key, e.Order, prev)
		}
		prev = e.Order
		if e.Group != "" || e.SubtitleKey != "" || e.VisibleIf != "" || e.InNav {
			t.Errorf("rail rows carry no group/subtitle/predicate/InNav (nav.html draws none): %+v", e)
		}
		if e.LabelFallback != "" || e.IconFallback != "" {
			t.Errorf("fallbacks are set by Resolve, never declared: %+v", e)
		}
	}
	if _, ok := CoreRailEntry("/orders"); !ok {
		t.Error("CoreRailEntry must find a declared key")
	}
	if _, ok := CoreRailEntry("/tables"); ok {
		t.Error("CoreRailEntry must not find a Menu-slot key")
	}
	if _, ok := CoreRailEntry("/catalog"); ok {
		t.Error("CoreRailEntry must not find an Items-slot key")
	}
}

// ADR-0088 Decision J: "/" and "/menu" are the only two rail entries with
// no equivalent tile anywhere in CoreMenu — losing either is a dead end,
// not an inconvenience. /inventory and /orders are independently reachable
// (a CoreMenu tile; the Items slot's own Inventory row) and stay
// unprotected.
func TestProtectedRailKeys_AreSellAndMenuOnly(t *testing.T) {
	if len(ProtectedRailKeys) != 2 || ProtectedRailKeys[0] != "/" || ProtectedRailKeys[1] != "/menu" {
		t.Fatalf("ADR-0088 Decision J names exactly / and /menu, got %v", ProtectedRailKeys)
	}
	for _, k := range []string{"/", "/menu"} {
		if !IsProtectedRailKey(k) {
			t.Errorf("%q must be a protected rail key", k)
		}
	}
	for _, k := range []string{"/inventory", "/orders", "/journal"} {
		if IsProtectedRailKey(k) {
			t.Errorf("%q must not be a protected rail key", k)
		}
	}
	// Protected sets are per slot: /menu is protected on the rail, not in
	// the Menu slot (where it isn't even a key), and /journal is protected
	// in the Menu slot but is not a rail key at all.
	if IsProtectedMenuKey("/menu") || IsProtectedItemsKey("/") {
		t.Error("rail protection must not leak into the other slots' protected sets")
	}
}

// The rail slot accepts exactly what the Items slot does today: reorder
// and relabel (the two amendment kinds nav.html can render without a
// restore surface or a group heading).
func TestParseRailAmendments_RelabelAndReorder(t *testing.T) {
	got, err := ParseRailAmendments("com.example.layout", map[string]any{
		"slot": "rail",
		"amendments": []any{
			map[string]any{"key": "/orders", "label_key": "layout.salon.services", "order": float64(250)},
		},
	})
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if len(got) != 1 || got[0].Slot != RailSlot || got[0].Key != "/orders" || got[0].LabelKey != "layout.salon.services" || *got[0].Order != 250 {
		t.Fatalf("parsed amendment mismatch: %+v", got)
	}
}

// Decision J: hide and icon stay refused for the WHOLE rail slot (same
// deferral as ItemsSlot — no findability/restore surface for a hidden rail
// entry exists), and group is refused because nav.html draws no heading.
// The refusal comes from parseAmendment's existing generic capability check
// wired through railSpec — no rail-specific refusal code.
func TestParseRailAmendments_RefusesHideIconGroup(t *testing.T) {
	cases := []struct {
		name   string
		amend  map[string]any
		wantIn string
	}{
		{"hide", map[string]any{"key": "/orders", "hide": true}, "hide"},
		{"icon", map[string]any{"key": "/orders", "icon": "scissors"}, "icon"},
		{"group", map[string]any{"key": "/orders", "group": "g.k"}, "group"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRailAmendments("com.example.layout", map[string]any{
				"amendments": []any{tc.amend},
			})
			if err == nil {
				t.Fatalf("%s must be refused on the rail slot", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) || !strings.Contains(err.Error(), "/orders") {
				t.Fatalf("error should name %q and the key, got: %v", tc.wantIn, err)
			}
		})
	}
}

// Decision J's protected pair: relabelling "/" or "/menu" is refused (a
// relabel is how a merchant recognises the only way back to the sale
// screen / the only way to everything else), reordering them is allowed
// (moving Sell/Menu within the rail hides nothing).
func TestParseRailAmendments_ProtectedKeysCannotBeRelabelledButCanBeReordered(t *testing.T) {
	for _, key := range ProtectedRailKeys {
		_, err := ParseRailAmendments("p", map[string]any{
			"amendments": []any{map[string]any{"key": key, "label_key": "x.y"}},
		})
		if err == nil || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "re-labelled") {
			t.Errorf("relabelling protected rail key %q must be refused naming it, got %v", key, err)
		}
		got, err := ParseRailAmendments("p", map[string]any{
			"amendments": []any{map[string]any{"key": key, "order": 500}},
		})
		if err != nil || len(got) != 1 || *got[0].Order != 500 {
			t.Errorf("reordering protected rail key %q must be allowed, got %+v, %v", key, got, err)
		}
	}
	// And an unprotected rail key stays relabel-able — the protection is
	// the two keys, not the slot.
	if _, err := ParseRailAmendments("p", map[string]any{
		"amendments": []any{map[string]any{"key": "/inventory", "label_key": "x.y"}},
	}); err != nil {
		t.Errorf("relabelling an unprotected rail key must be allowed, got %v", err)
	}
}

// A Menu-slot or Items-slot key is not a declared rail destination.
func TestParseRailAmendments_RefusesOtherSlotsKeys(t *testing.T) {
	for _, key := range []string{"/tables", "/catalog"} {
		_, err := ParseRailAmendments("p", map[string]any{
			"amendments": []any{map[string]any{"key": key, "order": 1}},
		})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("expected a refusal naming %s, got %v", key, err)
		}
	}
}

// ParseAmendmentsJSON routes "slot":"rail" to the rail spec, and its
// unsupported-slot error now names all three slots (the ut-docs#1911 review
// made the point that this message must not under-report what the
// dispatcher supports).
func TestParseAmendmentsJSON_DispatchesRailSlot(t *testing.T) {
	rail, err := ParseAmendmentsJSON("p", `{"slot":"rail","amendments":[{"key":"/orders","order":250}]}`)
	if err != nil || len(rail) != 1 || rail[0].Slot != RailSlot {
		t.Fatalf("rail dispatch: got %+v, %v", rail, err)
	}
	_, err = ParseAmendmentsJSON("p", `{"slot":"bogus","amendments":[]}`)
	if err == nil {
		t.Fatal("an unknown slot must be refused")
	}
	for _, name := range []string{MenuSlot, ItemsSlot, RailSlot} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("unsupported-slot error must name %q, got: %v", name, err)
		}
	}
}

// Decision I for the rail: nav.html renders on EVERY page, so the
// zero-amendment path must hand back CoreRail itself — no copy, no sort,
// no allocation. The falsifiable assertion is the Test; the Benchmark
// reports the same number for `go test -bench`.
func TestResolve_ZeroAmendmentsAllocatesNothing_Rail(t *testing.T) {
	in := CoreRail
	var sink []Entry
	allocs := testing.AllocsPerRun(1000, func() { sink = Resolve(in, nil) })
	if allocs != 0 {
		t.Fatalf("zero-amendment rail Resolve allocated %v times per run, want 0 (ADR-0088 Decision I)", allocs)
	}
	if len(sink) != len(CoreRail) || &sink[0] != &CoreRail[0] {
		t.Fatal("zero amendments must return CoreRail itself")
	}
}

func BenchmarkResolve_ZeroAmendments_Rail(b *testing.B) {
	in := CoreRail
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Resolve(in, nil)
	}
}
