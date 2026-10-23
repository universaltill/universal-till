package discovery

import (
	"context"
	"testing"
)

// ut-docs#4836: only a pairing (paired_at + store_id + token) counts; the
// store_id and token an anonymous registration writes do not.
func TestPairedWithCloudShop(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		kv   map[string]string
		want bool
	}{
		{"empty", nil, false},
		{"anonymous registration: store and token only", map[string]string{cloudStoreIDSettingKey: "s", cloudTokenSettingKey: "t"}, false},
		{"paired_at without token", map[string]string{CloudPairedAtSettingKey: "2026-10-22T10:00:00Z", cloudStoreIDSettingKey: "s"}, false},
		{"paired_at without store", map[string]string{CloudPairedAtSettingKey: "2026-10-22T10:00:00Z", cloudTokenSettingKey: "t"}, false},
		{"paired", map[string]string{CloudPairedAtSettingKey: "2026-10-22T10:00:00Z", cloudStoreIDSettingKey: "s", cloudTokenSettingKey: "t"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings := openFastFileSettings(t)
			for k, v := range c.kv {
				if err := settings.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			if got := PairedWithCloudShop(ctx, settings); got != c.want {
				t.Fatalf("PairedWithCloudShop = %v, want %v", got, c.want)
			}
		})
	}
}
