package config

import "testing"

// ut-docs#3814: UT_LAN_DISCOVERY switches mDNS advertising and browsing.
// Default on, and an unparseable value is on too — a typo must never turn
// discovery off on a production till.
func TestInitLANDiscovery(t *testing.T) {
	cases := []struct {
		name string
		val  *string
		want bool
	}{
		{name: "unset", want: true},
		{name: "one", val: strp("1"), want: true},
		{name: "true", val: strp("true"), want: true},
		{name: "zero", val: strp("0"), want: false},
		{name: "false", val: strp("false"), want: false},
		{name: "FALSE", val: strp("FALSE"), want: false},
		{name: "empty", val: strp(""), want: true},
		{name: "garbage", val: strp("nope"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetForTest(t, configEnvKeys)
			if tc.val != nil {
				t.Setenv("UT_LAN_DISCOVERY", *tc.val)
			}
			cfg, err := Init()
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if cfg.LANDiscovery != tc.want {
				t.Errorf("LANDiscovery = %v, want %v", cfg.LANDiscovery, tc.want)
			}
		})
	}
}
