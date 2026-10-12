package pluginview

import (
	"strings"
	"testing"
)

// ut-docs#2838: a confirm component carries a proposal id and nothing
// else — a plugin cannot put a word on core's confirm card.
func TestConfirm_OnlyAProposalID_2838(t *testing.T) {
	id := strings.Repeat("a", 32)
	ok := `{"document":{"version":1,"components":[{"type":"confirm","proposal":"` + id + `"}]}}`
	doc, err := DecodeViewAnswer([]byte(ok), Context{PluginID: "p"})
	if err != nil {
		t.Fatalf("valid confirm refused: %v", err)
	}
	if v := doc.Prepare("en"); v.Components[0].Proposal != id || v.Components[0].Confirm != nil {
		t.Fatalf("prepared = %+v", v.Components[0])
	}
	confirm := func(extra string) string {
		return `{"type":"confirm","proposal":"` + id + `"` + extra + `}`
	}
	for name, c := range map[string]string{
		"label":      confirm(`,"label":{"literal":"Harmless tidy-up"}`),
		"title":      confirm(`,"title":"Nothing important"`),
		"rows":       confirm(`,"rows":[["a","b"]]`),
		"route":      confirm(`,"action":"/plugin/x/confirm"`),
		"bad id":     `{"type":"confirm","proposal":"../../ui/agent"}`,
		"too many":   confirm("") + "," + confirm("") + "," + confirm("") + "," + confirm(""),
		"missing id": `{"type":"confirm"}`,
		"upper-hex":  `{"type":"confirm","proposal":"` + strings.Repeat("A", 32) + `"}`,
	} {
		raw := `{"document":{"version":1,"components":[` + c + `]}}`
		if _, err := DecodeViewAnswer([]byte(raw), Context{PluginID: "p"}); err == nil {
			t.Errorf("%s: accepted %s", name, c)
		}
	}
}

func TestResolveConfirms_NilIsNoLongerValid_2838(t *testing.T) {
	v := View{Components: []ViewComponent{{Type: "confirm", Proposal: "x"}, {Type: "text"}}}
	calls := 0
	v.ResolveConfirms(func(id string) *ViewConfirm { calls++; return nil })
	if calls != 1 || v.Components[0].Confirm != nil {
		t.Fatalf("calls = %d, card = %+v", calls, v.Components[0].Confirm)
	}
}
