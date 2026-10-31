package pluginview

import (
	"strings"
	"testing"
)

// ut-docs#4128: item_list names item ids (and which gaps the list came
// from); core draws the names, thumbnails and links, so a plugin can put
// neither a word nor a URL on it.
func TestItemList_OnlyIDsAndMask_4128(t *testing.T) {
	decode := func(c string) error {
		_, err := DecodeViewAnswer([]byte(`{"document":{"version":1,"components":[`+c+`]}}`), Context{PluginID: "p"})
		return err
	}
	if err := decode(`{"type":"item_list","items":["a1","b-2"],"missing":1}`); err != nil {
		t.Fatalf("valid item_list refused: %v", err)
	}
	if err := decode(`{"type":"item_list","items":["a1"]}`); err != nil {
		t.Fatalf("item_list without missing refused: %v", err)
	}
	many := make([]string, MaxItemListItems+1)
	for i := range many {
		many[i] = "i" + strings.Repeat("x", i%3)
	}
	for name, c := range map[string]string{
		"label":      `{"type":"item_list","items":["a"],"label":"Harmless"}`,
		"url":        `{"type":"item_list","items":["a"],"href":"https://evil.example"}`,
		"empty":      `{"type":"item_list","items":[]}`,
		"bad id":     `{"type":"item_list","items":["../x"]}`,
		"long id":    `{"type":"item_list","items":["` + strings.Repeat("a", 65) + `"]}`,
		"too many":   `{"type":"item_list","items":["` + strings.Join(many, `","`) + `"]}`,
		"mask high":  `{"type":"item_list","items":["a"],"missing":16}`,
		"mask minus": `{"type":"item_list","items":["a"],"missing":-1}`,
		"four lists": strings.Repeat(`{"type":"item_list","items":["a"]},`, 4)[:len(strings.Repeat(`{"type":"item_list","items":["a"]},`, 4))-1],
	} {
		if err := decode(c); err == nil {
			t.Errorf("%s: accepted %s", name, c)
		}
	}
}

func TestResolveItemLists_NilHidesTheList_4128(t *testing.T) {
	doc, err := DecodeViewAnswer([]byte(`{"document":{"version":1,"components":[{"type":"item_list","items":["a"],"missing":2}]}}`), Context{PluginID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	v := doc.Prepare("en")
	var gotIDs []string
	var gotMask int
	v.ResolveItemLists(func(ids []string, missing int) *ViewItemList { gotIDs, gotMask = ids, missing; return nil })
	if len(gotIDs) != 1 || gotMask != 2 || v.Components[0].ItemList != nil {
		t.Fatalf("ids %v mask %d list %+v", gotIDs, gotMask, v.Components[0].ItemList)
	}
}
