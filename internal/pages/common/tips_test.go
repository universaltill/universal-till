package common

import (
	"context"
	"testing"
)

type tipStore map[string]string

func (s tipStore) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := s[key]
	return v, ok, nil
}

// payments.tips_enabled: an explicit choice wins; unset follows the shop
// type (on for a service shop, off otherwise).
func TestTipsEnabled(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  map[string]string
		want bool
	}{
		{"unset, no shop type: off", map[string]string{}, false},
		{"unset, retail: off", map[string]string{"shop.type": "retail"}, false},
		{"unset, hospitality: off", map[string]string{"shop.type": "hospitality"}, false},
		{"unset, service: on", map[string]string{"shop.type": "service"}, true},
		{"explicit off beats service", map[string]string{"shop.type": "service", KeyTipsEnabled: "0"}, false},
		{"explicit on beats retail", map[string]string{"shop.type": "retail", KeyTipsEnabled: "1"}, true},
		{"explicit true spelling", map[string]string{KeyTipsEnabled: "true"}, true},
		{"explicit false spelling", map[string]string{"shop.type": "service", KeyTipsEnabled: "false"}, false},
		{"unrecognised value falls back to the shop type", map[string]string{"shop.type": "service", KeyTipsEnabled: "maybe"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TipsEnabled(t.Context(), tipStore(tc.set)); got != tc.want {
				t.Fatalf("TipsEnabled = %v, want %v", got, tc.want)
			}
		})
	}
	if TipsEnabled(t.Context(), nil) {
		t.Fatal("a nil store must read as off")
	}
}

func TestTipPresetBPs(t *testing.T) {
	if got := TipPresetBPs(t.Context(), tipStore{}); len(got) != 3 || got[0] != 1000 || got[2] != 2000 {
		t.Fatalf("unset presets = %v, want 10/15/20 percent", got)
	}
	if got := TipPresetBPs(t.Context(), tipStore{KeyTipPresets: "5, 12.5"}); len(got) != 2 || got[1] != 1250 {
		t.Fatalf("custom presets = %v, want [500 1250]", got)
	}
}
