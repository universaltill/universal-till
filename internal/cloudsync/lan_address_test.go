package cloudsync

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2774: only an actual main till (no sync.primary_url — ADR-0011's
// single source of truth) reports its LAN address to the cloud. The address
// rides DeviceExtra, but the gate lives here, next to where the role is
// decided, so a replica never sends one whatever its hooks contribute —
// and, per the code-review finding on this card, a backoffice-mode till
// still reports when it is itself the main till (no primary_url: the
// reported "role" string is "backoffice" but it is not a replica), while a
// backoffice-mode till that IS a replica (primary_url set — "backoffice
// wins over replica" in the reported role string, TestTickRolesFollowSettings)
// still never reports.
func TestBuildSyncRequestLANAddressOnlyFromPrimary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		wantRole string
		wantAddr bool
	}{
		{"primary", nil, "primary", true},
		{"replica", map[string]string{"sync.primary_url": "http://192.168.1.20:8080"}, "replica", false},
		{"backoffice-mode main till (no primary_url)", map[string]string{"display.mode": "backoffice"}, "backoffice", true},
		{"backoffice-mode replica (primary_url set)", map[string]string{"display.mode": "backoffice", "sync.primary_url": "http://192.168.1.20:8080"}, "backoffice", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := data.NewSettingsRepo(testDB(t))
			ctx := context.Background()
			for k, v := range tc.settings {
				if err := settings.Set(ctx, k, v); err != nil {
					t.Fatal(err)
				}
			}
			hooks := Hooks{DeviceExtra: func(context.Context) map[string]any {
				return map[string]any{LANAddressKey: "192.168.1.30:8080", "theme": "monarch"}
			}}
			dev := buildSyncRequest(ctx, testCfg(""), settings, hooks).devices[0]
			if dev["role"] != tc.wantRole {
				t.Fatalf("role = %v, want %s", dev["role"], tc.wantRole)
			}
			_, has := dev[LANAddressKey]
			if has != tc.wantAddr {
				t.Fatalf("lan_address present = %v, want %v (device %+v)", has, tc.wantAddr, dev)
			}
			if dev["theme"] != "monarch" {
				t.Fatal("other DeviceExtra fields were dropped")
			}
		})
	}
}
