package pages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#7206: sale.completed line_items[] echo the per-line ref and the
// plugin that set it; absent = the keys are not on the wire at all.
func TestSaleCompletedEvent_LineRefEcho_7206(t *testing.T) {
	ev := saleCompletedEventFor(data.SaleDetail{
		ID: "s1",
		Lines: []data.SaleDetailLine{
			{SKU: "SVC", Qty: 1, UnitPrice: 2500, PluginRef: "appt:1:0", PluginRefBy: "com.example.page"},
			{SKU: "SVC", Qty: 1, UnitPrice: 2500},
		},
	})
	if got := ev.LineItems[0]; got.Ref != "appt:1:0" || got.RefPlugin != "com.example.page" {
		t.Errorf("line 0 = %+v, want ref and ref_plugin", got)
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		LineItems []map[string]any `json:"line_items"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.LineItems[0]["ref"] != "appt:1:0" || back.LineItems[0]["ref_plugin"] != "com.example.page" {
		t.Errorf("wire line 0 = %v", back.LineItems[0])
	}
	if _, has := back.LineItems[1]["ref"]; has || strings.Contains(string(raw), `"ref":""`) {
		t.Errorf("a line without a ref must not carry the key: %v", back.LineItems[1])
	}
	if _, has := back.LineItems[1]["ref_plugin"]; has {
		t.Errorf("a line without a ref must not carry ref_plugin: %v", back.LineItems[1])
	}
}
