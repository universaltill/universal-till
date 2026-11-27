package shoptemplate

import (
	"testing"
	"testing/fstest"
)

func TestLoadRejectsUnknownField(t *testing.T) {
	err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"surprise":1`)})
	wantErrContains(t, err, "surprise")
}

func TestLoadRejectsUnknownFieldInsideAValue(t *testing.T) {
	err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"menu":{"/tables":{"colour":"red"}}`)})
	wantErrContains(t, err, "colour")
}

func TestLoadRejectsWrongSchemaVersion(t *testing.T) {
	err := loadErr(t, map[string]string{"retail": `{"schema":2,"id":"retail","version":1,"shop_types":["retail"],"label_key":"lbl","summary_key":"sum"}`})
	wantErrContains(t, err, "schema")
}

func TestLoadRejectsBadIDOrVersion(t *testing.T) {
	cases := map[string]string{
		"empty id":     `{"schema":1,"id":"","version":1,"shop_types":["retail"],"label_key":"lbl","summary_key":"sum"}`,
		"upper id":     `{"schema":1,"id":"Retail","version":1,"shop_types":["retail"],"label_key":"lbl","summary_key":"sum"}`,
		"zero version": `{"schema":1,"id":"retail","version":0,"shop_types":["retail"],"label_key":"lbl","summary_key":"sum"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			loadErr(t, map[string]string{"retail": body})
		})
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	wantErrContains(t, loadErr(t, map[string]string{"retail": `{`}), "retail.json")
}

func TestLoadRequiresATemplateForEveryShopType(t *testing.T) {
	err := loadErr(t, map[string]string{"market_stall": ""})
	wantErrContains(t, err, "market_stall")
}

func TestLoadRejectsDuplicateShopTypeAcrossTemplates(t *testing.T) {
	err := loadErr(t, map[string]string{"extra": tmpl("extra", []string{"retail"}, "")})
	wantErrContains(t, err, "retail")
	wantErrContains(t, err, "more than one")
}

func TestLoadRejectsUnknownShopType(t *testing.T) {
	err := loadErr(t, map[string]string{"extra": tmpl("extra", []string{"spaceport"}, "")})
	wantErrContains(t, err, "spaceport")
}

func TestLoadRejectsTemplateWithoutShopTypes(t *testing.T) {
	loadErr(t, map[string]string{"extra": `{"schema":1,"id":"extra","version":1,"shop_types":[],"label_key":"lbl","summary_key":"sum"}`})
}

func TestLoadRejectsUnknownKeyInEachKind(t *testing.T) {
	for _, c := range []struct{ member, key string }{
		{"settings", "settings-nope"},
		{"settings_layout", "settings-nope"},
		{"menu", "/nope"},
		{"items", "/nope"},
		{"rail", "/nope"},
	} {
		t.Run(c.member, func(t *testing.T) {
			err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"`+c.member+`":{"`+c.key+`":"common"}`)})
			wantErrContains(t, err, c.key)
		})
	}
}

func TestLoadRejectsInvalidClass(t *testing.T) {
	err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"settings":{"settings-theme":"hidden"}`)})
	wantErrContains(t, err, "hidden")
}

func TestLoadRejectsFixedClassKeyGivenAClass(t *testing.T) {
	for _, extra := range []string{
		`"settings":{"settings-payments":"advanced"}`,
		`"menu":{"/settings":"advanced"}`,
		`"menu":{"/settings":{"class":"advanced"}}`,
	} {
		err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, extra)})
		wantErrContains(t, err, "fixed")
	}
}

func TestLoadAllowsReorderOfAFixedKeyWithoutClass(t *testing.T) {
	s := mustLoad(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"menu":{"/settings":{"order":5}}`)})
	am := s.ForType("retail").Amendments("menu")
	if len(am) != 1 || am[0].Key != "/settings" || am[0].Order == nil || *am[0].Order != 5 || am[0].Hide {
		t.Fatalf("amendments = %+v, want one reorder of /settings to 5 without hide", am)
	}
}

func TestLoadRejectsLegacyHideOfAFixedKey(t *testing.T) {
	err := loadErr(t, map[string]string{"service": tmpl("service", []string{"service"}, `"legacy_layout":{"menu":[{"key":"/settings","hide":true}]}`)})
	wantErrContains(t, err, "fixed")
}

func TestLoadRejectsDemotingRailAndItems(t *testing.T) {
	for _, extra := range []string{`"rail":{"/orders":"advanced"}`, `"items":{"/catalog":"advanced"}`} {
		err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, extra)})
		wantErrContains(t, err, "cannot be demoted")
	}
}

func TestLoadRejectsSuggestOutsideCapabilitySet(t *testing.T) {
	// a plugin id and a country code are both outside the closed capability set
	for _, bad := range []string{"com.universaltill.pay-x", "DE"} {
		err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"suggest":["`+bad+`"]`)})
		wantErrContains(t, err, bad)
	}
	mustLoad(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"suggest":["printing"]`)})
}

func TestLoadSeedRules(t *testing.T) {
	ok := func(seed string) {
		t.Helper()
		mustLoad(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"seed":[`+seed+`]`)})
	}
	bad := func(seed, want string) {
		t.Helper()
		wantErrContains(t, loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"seed":[`+seed+`]`)}), want)
	}
	ok(`{"key":"sale.order_type_prompt","value":"off"}`)
	ok(`{"key":"theme","value":"classic"}`) // the one per-till exception: no registry row needed
	bad(`{"key":"payments.provider","value":"x"}`, "whitelist")
	bad(`{"key":"sale.sensitive","value":"x"}`, "whitelist")
	bad(`{"key":"sale.order_type_prompt","value":"sideways"}`, "sideways")
	bad(`{"key":"sale.order_type_prompt","value":"off"},{"key":"sale.order_type_prompt","value":"top"}`, "twice")
}

func TestLoadRejectsSeedMissingFromRegistryNormalTier(t *testing.T) {
	cat := newFakeCatalog()
	delete(cat.normal, "sale.order_type_prompt")
	_, err := loadFS(allTypesFS(map[string]string{"retail": tmpl("retail", []string{"retail"}, `"seed":[{"key":"sale.order_type_prompt","value":"off"}]`)}), cat)
	if err == nil {
		t.Fatal("want error for a seed key that is not registry tier normal")
	}
	wantErrContains(t, err, "normal")
}

func TestLoadRejectsMissingEnglishKeys(t *testing.T) {
	cases := map[string]string{
		"label":   `{"schema":1,"id":"retail","version":1,"shop_types":["retail"],"label_key":"nope.label","summary_key":"sum"}`,
		"summary": `{"schema":1,"id":"retail","version":1,"shop_types":["retail"],"label_key":"lbl","summary_key":"nope.summary"}`,
		"objlbl":  tmpl("retail", []string{"retail"}, `"menu":{"/items":{"label_key":"nope.item"}}`),
		"group":   tmpl("retail", []string{"retail"}, `"settings_layout":{"settings-printer":{"group":"nope.group"}}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			err := loadErr(t, map[string]string{"retail": body})
			wantErrContains(t, err, "nope.")
		})
	}
}

func TestLoadRejectsUnknownIcon(t *testing.T) {
	err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"menu":{"/items":{"icon":"no-such-icon-xyz"}}`)})
	wantErrContains(t, err, "no-such-icon-xyz")
}

func TestExtendsChildOverridesParentPerKey(t *testing.T) {
	parent := tmpl("cafe", []string{"cafe"}, `"settings":{"settings-theme":"advanced","settings-printer":"advanced"},"seed":[{"key":"sale.display_no_scheme","value":"lifetime_no_reset"}],"menu":{"/tables":"advanced"}`)
	child := tmpl("hospitality", []string{"hospitality"}, `"extends":"cafe","settings":{"settings-printer":"essential"},"menu":{"/tables":"common"}`)
	s := mustLoad(t, map[string]string{"cafe": parent, "hospitality": child})
	r := s.ForType("hospitality")
	if got := r.ClassFor(KindSettings, "settings-theme", "common"); got != "advanced" {
		t.Fatalf("inherited settings-theme class = %q, want advanced (from parent)", got)
	}
	if got := r.ClassFor(KindSettings, "settings-printer", "common"); got != "essential" {
		t.Fatalf("overridden settings-printer class = %q, want essential (child wins)", got)
	}
	if got := r.ClassFor(KindMenu, "/tables", "advanced"); got != "common" {
		t.Fatalf("overridden /tables class = %q, want common", got)
	}
	if seeds := r.Seeds(); len(seeds) != 1 || seeds[0] != (Seed{Key: "sale.display_no_scheme", Value: "lifetime_no_reset"}) {
		t.Fatalf("seeds = %+v, want the parent's one seed inherited", seeds)
	}
	if got := s.ForType("cafe").ClassFor(KindSettings, "settings-printer", "common"); got != "advanced" {
		t.Fatalf("parent must be unaffected by its child, got %q", got)
	}
}

func TestExtendsRejectsUnknownParentCycleAndDepth(t *testing.T) {
	// unknown parent
	wantErrContains(t, loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"extends":"ghost"`)}), "ghost")
	// self
	wantErrContains(t, loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"extends":"retail"`)}), "itself")
	// mutual cycle
	err := loadErr(t, map[string]string{
		"retail": tmpl("retail", []string{"retail"}, `"extends":"cafe"`),
		"cafe":   tmpl("cafe", []string{"cafe"}, `"extends":"retail"`),
	})
	wantErrContains(t, err, "one level")
	// two levels deep without a cycle
	err = loadErr(t, map[string]string{
		"retail":      tmpl("retail", []string{"retail"}, `"extends":"cafe"`),
		"cafe":        tmpl("cafe", []string{"cafe"}, `"extends":"hospitality"`),
		"hospitality": tmpl("hospitality", []string{"hospitality"}, ""),
	})
	wantErrContains(t, err, "one level")
}

func TestLoadNeedsACatalog(t *testing.T) {
	if _, err := loadFS(fstest.MapFS{}, nil); err == nil {
		t.Fatal("nil catalog must be an error, never a silent skip of validation")
	}
}

func TestThemeAndIconPackAreLoadedAndIgnored(t *testing.T) {
	s := mustLoad(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"theme":"classic","icon_pack":"outline"`)})
	r := s.ForType("retail")
	if r == nil || len(r.Seeds()) != 0 || len(r.Amendments("menu")) != 0 {
		t.Fatalf("theme/icon_pack must not produce seeds or amendments: %+v", r)
	}
}

func TestLoadRejectsEntryWithNeitherClassNorFields(t *testing.T) {
	for _, extra := range []string{`"settings":{"settings-theme":""}`, `"menu":{"/items":{}}`} {
		err := loadErr(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, extra)})
		wantErrContains(t, err, "neither a class nor any field")
	}
}
