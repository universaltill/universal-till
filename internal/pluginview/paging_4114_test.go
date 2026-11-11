package pluginview

import (
	"strings"
	"testing"
)

// ut-docs#4114: a content slot panel's paging links keep core's own "slot"
// param and carry the panel they swap into.

const pagedTable4114 = `{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":{"page":2,"pages":5}}`

func TestLinkPagingPanel_KeepsSlotAndTarget_4114(t *testing.T) {
	v := prepared(t, pagedTable4114, "en")
	v.LinkPagingPanel("/plugin/demo", map[string]string{"days": "7", "_job": "x", "page": "2", "slot": "other"}, "reports.panels", "plugin-slot-reports-panels-0")
	p := v.Components[0].Paging
	if p.Target != "plugin-slot-reports-panels-0" {
		t.Errorf("Target = %q", p.Target)
	}
	if want := "/plugin/demo?days=7&page=1&slot=reports.panels"; p.PrevURL != want {
		t.Errorf("prev = %q, want %q", p.PrevURL, want)
	}
	if want := "/plugin/demo?days=7&page=3&slot=reports.panels"; p.NextURL != want {
		t.Errorf("next = %q, want %q", p.NextURL, want)
	}
	// A page's links stay plain.
	w := prepared(t, pagedTable4114, "en")
	w.LinkPaging("/plugin/demo", map[string]string{"days": "7"})
	if w.Components[0].Paging.Target != "" || strings.Contains(w.Components[0].Paging.NextURL, "slot") {
		t.Errorf("page links = %+v", w.Components[0].Paging)
	}
}

func TestLinkPagingPanel_CapKeepsParamAndSlot_4114(t *testing.T) {
	v := prepared(t, pagedTable4114, "en")
	params := map[string]string{}
	for i := 0; i < 25; i++ {
		params["a"+itoa(i+10)] = "v"
	}
	v.LinkPagingPanel("/plugin/demo", params, "reports.panels", "plugin-slot-reports-panels-0")
	for _, u := range []string{v.Components[0].Paging.PrevURL, v.Components[0].Paging.NextURL} {
		if n := strings.Count(u, "="); n > MaxParams || !strings.Contains(u, "page=") || !strings.Contains(u, "slot=reports.panels") {
			t.Errorf("link %q has %d keys, want <= %d incl. page and slot", u, n, MaxParams)
		}
	}
}

func TestLinkPagingPanel_SlotParamTableIndicatorOnly_4114(t *testing.T) {
	v := prepared(t, `{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["x"]],"paging":{"page":2,"pages":5,"param":"slot"}}`, "en")
	v.LinkPagingPanel("/plugin/demo", nil, "reports.panels", "plugin-slot-reports-panels-0")
	p := v.Components[0].Paging
	if p.PrevURL != "" || p.NextURL != "" || p.Text == "" {
		t.Errorf("slot-param table = %+v, want indicator only", p)
	}
}
