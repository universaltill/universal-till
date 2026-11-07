package catsync

import (
	"encoding/json"
	"testing"
)

// ut-docs#5858: imported names share data.CheckSingleLineName with the admin
// screens: RTL marks and balanced isolates are accepted, bidi overrides and
// U+2028/2029 are refused. ExternalID and SKU keep the stricter cleanText.
func TestParseRow_NameCharacters(t *testing.T) {
	cases := []struct {
		label, name string
		wantOK      bool
	}{
		{"rlm accepted", "שם‏ 1", true},
		{"balanced isolate accepted", "⁧שם⁩ 1L", true},
		{"rlo refused", "Co‮la", false},
		{"line separator refused", "Co la", false},
		{"paragraph separator refused", "Co la", false},
		{"zwsp refused", "Co​la", false},
		{"unbalanced isolate refused", "⁧x", false},
	}
	for _, c := range cases {
		for _, field := range []string{"name", "category_path"} {
			t.Run(c.label+" "+field, func(t *testing.T) {
				nm, cat := "Cola", "null"
				if field == "name" {
					nm = c.name
				} else {
					b, _ := json.Marshal([]string{c.name})
					cat = string(b)
				}
				nb, _ := json.Marshal(nm)
				raw := `{"external_id":"a","name":` + string(nb) + `,"price_minor":1,"category_path":` + cat + `}`
				_, _, is := parseRow(json.RawMessage(raw))
				if is.none() != c.wantOK {
					t.Fatalf("issue = %+v, accepted = %v, want %v", is, is.none(), c.wantOK)
				}
			})
		}
	}
	// ExternalID keeps cleanText: no RTL marks.
	if _, _, is := parseRow(json.RawMessage(`{"external_id":"a‏","name":"x","price_minor":1}`)); is.none() {
		t.Fatal("external_id with RLM should stay refused")
	}
}
