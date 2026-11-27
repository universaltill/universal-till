package registrydesc

import (
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

func TestClassForRegion(t *testing.T) {
	cases := []struct {
		region string
		want   uislot.Class
		ok     bool
	}{
		{"settings-payments", uislot.ClassEssential, true},
		{"settings-stock-tracking", uislot.ClassCommon, true},
		{"settings-barcode", uislot.ClassAdvanced, true},
		{"settings-currency-display", uislot.ClassEssential, true}, // inherits settings-currency
		{"receipt-designer", uislot.ClassCommon, true},             // follows settings-printer
		{"no-such-card", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := ClassForRegion(c.region)
		if got != c.want || ok != c.ok {
			t.Errorf("ClassForRegion(%q) = (%q,%v), want (%q,%v)", c.region, got, ok, c.want, c.ok)
		}
	}
}

func TestRegionCardTargetsExist(t *testing.T) {
	for region, card := range regionCard {
		if _, ok := uislot.CoreSettingsEntry(card); !ok {
			t.Errorf("regionCard[%q] = %q is not a CoreSettings row", region, card)
		}
		if _, direct := uislot.CoreSettingsEntry(region); direct {
			t.Errorf("regionCard[%q] is redundant: it is itself a CoreSettings row", region)
		}
	}
}
