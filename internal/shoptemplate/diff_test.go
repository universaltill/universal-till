package shoptemplate

import (
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

func diffSet(t *testing.T) *Set {
	t.Helper()
	return testSet(t,
		&Template{Schema: 1, ID: "calm", Version: 1, ShopTypes: []string{"retail"},
			Seed:     []Seed{{Key: "sale.order_type_prompt", Value: "off"}},
			Menu:     map[string]Value{"/tables": {Class: uislot.ClassAdvanced}, "/kitchen-stations": {Class: uislot.ClassAdvanced}},
			Settings: map[string]Value{"settings-theme": {Class: uislot.ClassAdvanced}, "settings-printer": {Class: uislot.ClassEssential}}},
		&Template{Schema: 1, ID: "busy", Version: 1, ShopTypes: []string{"cafe"},
			Seed:     []Seed{{Key: "sale.order_type_prompt", Value: "top"}, {Key: "sale.display_no_scheme", Value: "lifetime_no_reset"}},
			Menu:     map[string]Value{"/tables": {Class: uislot.ClassEssential}},
			Settings: map[string]Value{"settings-printer": {Class: uislot.ClassEssential}}},
	)
}

func keysOf(items []PreviewItem) []string {
	var out []string
	for _, i := range items {
		out = append(out, string(i.Kind)+":"+i.Key)
	}
	return out
}

func TestDiff_VisibilityGroups(t *testing.T) {
	s := diffSet(t)
	calm, busy := s.ForType("retail"), s.ForType("cafe")

	// calm -> busy: Tables comes back, Kitchen stations comes back (busy says
	// nothing, base is common), Theme leaves More settings.
	p := Diff(calm, busy, Ledger{}, nil, nil)
	if got, want := keysOf(p.Shown), []string{"menu:/kitchen-stations", "menu:/tables", "settings:settings-theme"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Shown = %v, want %v", got, want)
	}
	if len(p.Hidden) != 0 {
		t.Fatalf("Hidden = %v, want none", keysOf(p.Hidden))
	}

	// busy -> calm is the mirror: Kitchen stations and Theme move away.
	p = Diff(busy, calm, Ledger{}, nil, nil)
	if got, want := keysOf(p.Hidden), []string{"menu:/kitchen-stations", "menu:/tables", "settings:settings-theme"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Hidden = %v, want %v", got, want)
	}
	if len(p.Shown) != 0 {
		t.Fatalf("Shown = %v, want none", keysOf(p.Shown))
	}
}

func TestDiff_EssentialVsCommonIsNotAVisibilityChange(t *testing.T) {
	s := diffSet(t)
	p := Diff(s.ForType("retail"), s.ForType("cafe"), Ledger{}, nil, nil)
	for _, i := range append(p.Shown, p.Hidden...) {
		if i.Key == "settings-printer" {
			t.Fatalf("settings-printer is essential in both types, got %v", i)
		}
	}
}

func TestDiff_BaseAdvancedRowIsNotClaimedAsMoved(t *testing.T) {
	s := diffSet(t)
	base := func(k Kind, key string) uislot.Class {
		if k == KindMenu && key == "/kitchen-stations" {
			return uislot.ClassAdvanced // already hidden before any template
		}
		return uislot.ClassCommon
	}
	// nil -> calm: Kitchen was already advanced by its base class, so it does
	// not "move"; Tables does.
	p := Diff(nil, s.ForType("retail"), Ledger{}, nil, base)
	if got, want := keysOf(p.Hidden), []string{"menu:/tables", "settings:settings-theme"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Hidden = %v, want %v", got, want)
	}
}

func TestDiff_ValuesOnlyForKeysStillEqualToTheLedger(t *testing.T) {
	s := diffSet(t)
	calm, busy := s.ForType("retail"), s.ForType("cafe")
	ledger := Ledger{ID: "calm", Seeded: map[string]string{"sale.order_type_prompt": "off"}}

	// order_type_prompt untouched (== seeded "off"), display_no unset: both
	// are offered.
	p := Diff(calm, busy, ledger, map[string]string{"sale.order_type_prompt": "off"}, nil)
	want := []ValueChange{
		{Key: "sale.display_no_scheme", Now: "", Suggested: "lifetime_no_reset"},
		{Key: "sale.order_type_prompt", Now: "off", Suggested: "top"},
	}
	if !reflect.DeepEqual(p.Values, want) {
		t.Fatalf("Values = %+v, want %+v", p.Values, want)
	}
	if len(p.Kept) != 0 {
		t.Fatalf("Kept = %v, want none", p.Kept)
	}
}

func TestDiff_OwnerEditedValueIsKeptNeverOffered(t *testing.T) {
	s := diffSet(t)
	calm, busy := s.ForType("retail"), s.ForType("cafe")
	ledger := Ledger{ID: "calm", Seeded: map[string]string{"sale.order_type_prompt": "off"}}
	// The owner changed it to "at_pay" after the template seeded "off".
	p := Diff(calm, busy, ledger, map[string]string{"sale.order_type_prompt": "at_pay"}, nil)
	for _, v := range p.Values {
		if v.Key == "sale.order_type_prompt" {
			t.Fatalf("an owner-set value must never be offered: %+v", p.Values)
		}
	}
	if !reflect.DeepEqual(p.Kept, []string{"sale.order_type_prompt"}) {
		t.Fatalf("Kept = %v, want [sale.order_type_prompt]", p.Kept)
	}
}

func TestDiff_ValueAlreadyAtSuggestedIsOmitted(t *testing.T) {
	s := diffSet(t)
	p := Diff(nil, s.ForType("cafe"), Ledger{}, map[string]string{"sale.order_type_prompt": "top", "sale.display_no_scheme": "lifetime_no_reset"}, nil)
	if len(p.Values) != 0 || len(p.Kept) != 0 {
		t.Fatalf("nothing to offer or keep, got values=%+v kept=%v", p.Values, p.Kept)
	}
}

func TestDiff_NonWhitelistedSeedNeverOffered(t *testing.T) {
	s := testSet(t, &Template{Schema: 1, ID: "x", Version: 1, ShopTypes: []string{"retail"}})
	r := s.ForType("retail")
	r.seeds = []Seed{{Key: "payments.default_method", Value: "cash"}}
	p := Diff(nil, r, Ledger{}, nil, nil)
	if len(p.Values) != 0 {
		t.Fatalf("a payment key must never be offered: %+v", p.Values)
	}
}

func TestDiff_SameTypeIsEmpty(t *testing.T) {
	s := diffSet(t)
	p := Diff(s.ForType("retail"), s.ForType("retail"), Ledger{}, map[string]string{"sale.order_type_prompt": "off"}, nil)
	if !p.LabelOnly() {
		t.Fatalf("same template: want label-only preview, got %+v", p)
	}
}

func TestDiff_NilToNilIsLabelOnly(t *testing.T) {
	if !Diff(nil, nil, Ledger{}, nil, nil).LabelOnly() {
		t.Fatal("no template either side: label only")
	}
}

func TestDiff_Deterministic(t *testing.T) {
	s := diffSet(t)
	a := Diff(s.ForType("retail"), s.ForType("cafe"), Ledger{}, nil, nil)
	for i := 0; i < 20; i++ {
		if b := Diff(s.ForType("retail"), s.ForType("cafe"), Ledger{}, nil, nil); !reflect.DeepEqual(a, b) {
			t.Fatalf("diff order changed between runs:\n%+v\n%+v", a, b)
		}
	}
}
