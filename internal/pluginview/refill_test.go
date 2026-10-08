package pluginview

import "testing"

// refillView: a "save" form with every field kind, and a second form
// ("other") that a refill for "save" must leave alone (ut-docs#3879).
func refillView() View {
	opts := func() []ViewOption {
		return []ViewOption{{Value: "a", Label: "A", Selected: true}, {Value: "b", Label: "B"}}
	}
	return View{Components: []ViewComponent{
		{Type: "text", Text: "hello"},
		{Type: "form", Action: "save", Fields: []ViewField{
			{Name: "note", Kind: "text", Value: "default"},
			{Name: "qty", Kind: "number", Value: "1"},
			{Name: "price", Kind: "money", Value: "2.50"},
			{Name: "mode", Kind: "select", Value: "a", Options: opts()},
			{Name: "on", Kind: "toggle", Checked: false},
			{Name: "off", Kind: "toggle", Checked: true},
			{Name: "api_key", Kind: "secret"},
			{Name: "upload", Kind: "file"},
			{Name: "untouched", Kind: "text", Value: "keep"},
		}},
		{Type: "form", Action: "other", Fields: []ViewField{
			{Name: "note", Kind: "text", Value: "other default"},
			{Name: "on", Kind: "toggle", Checked: true},
		}},
	}}
}

func TestRefill_RestoresPostedValues_3879(t *testing.T) {
	v := refillView()
	v.Refill("save", map[string][]string{
		"_action": {"save"},
		"note":    {"typed <b>"},
		"qty":     {"3,5"},
		"price":   {"12,3x"}, // the raw string as typed, even if invalid
		"mode":    {"b"},
		"on":      {"true"},
		// "off" absent: an unchecked checkbox is not posted.
		"api_key": {"s3cret"},
		"upload":  {"c:\\fakepath\\x.csv"},
	})
	f := v.Components[1].Fields
	want := map[string]string{"note": "typed <b>", "qty": "3,5", "price": "12,3x", "mode": "b", "untouched": "keep"}
	for _, fl := range f {
		if w, ok := want[fl.Name]; ok && fl.Value != w {
			t.Errorf("%s = %q, want %q", fl.Name, fl.Value, w)
		}
	}
	if mode := f[3]; mode.Options[0].Selected || !mode.Options[1].Selected {
		t.Errorf("select options = %+v, want only b selected", mode.Options)
	}
	if !f[4].Checked {
		t.Error("posted toggle on=true not checked")
	}
	if f[5].Checked {
		t.Error("absent toggle must be unchecked")
	}
	if f[6].Value != "" || f[7].Value != "" {
		t.Errorf("secret/file refilled: %q / %q", f[6].Value, f[7].Value)
	}
	other := v.Components[2].Fields
	if other[0].Value != "other default" || !other[1].Checked {
		t.Errorf("a form with another action was refilled: %+v", other)
	}
	if v.Components[0].Text != "hello" {
		t.Error("a non-form component changed")
	}
}

func TestRefill_UnknownSelectValueKeepsDefault_3879(t *testing.T) {
	v := refillView()
	v.Refill("save", map[string][]string{"mode": {"zzz"}})
	mode := v.Components[1].Fields[3]
	if mode.Value != "a" || !mode.Options[0].Selected || mode.Options[1].Selected {
		t.Errorf("select = %+v, want the default kept for a value no option has", mode)
	}
}

func TestRefill_NoMatchingFormOrNothingPosted_3879(t *testing.T) {
	v := refillView()
	v.Refill("missing", map[string][]string{"note": {"x"}, "on": {"true"}})
	if f := v.Components[1].Fields; f[0].Value != "default" || f[4].Checked || !f[5].Checked {
		t.Errorf("refill for an action no form has changed the save form: %+v", f)
	}
	v = refillView()
	v.Refill("save", nil)
	if f := v.Components[1].Fields; f[0].Value != "default" || !f[5].Checked {
		t.Errorf("nil posted values must change nothing: %+v", f)
	}
}
