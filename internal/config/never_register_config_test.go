package config

import "testing"

// ADR-0200 section 5 (ut-docs#6559): UT_NEVER_REGISTER is the one switch that
// keeps a throwaway till (CI, e2e, docs-shots, device tests) out of the cloud
// fleet count. Default off; a set value fails SAFE: anything except an
// explicit false/0 means "never register", so a typo cannot mint a store.
func TestInitNeverRegister(t *testing.T) {
	cases := []struct {
		name string
		val  *string
		want bool
	}{
		{name: "unset", want: false},
		{name: "empty", val: strp(""), want: false},
		{name: "one", val: strp("1"), want: true},
		{name: "true", val: strp("true"), want: true},
		{name: "zero", val: strp("0"), want: false},
		{name: "false", val: strp("false"), want: false},
		{name: "garbage fails safe", val: strp("yes"), want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetForTest(t, configEnvKeys)
			if tc.val != nil {
				t.Setenv("UT_NEVER_REGISTER", *tc.val)
			}
			cfg, err := Init()
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if cfg.NeverRegister != tc.want {
				t.Errorf("NeverRegister = %v, want %v", cfg.NeverRegister, tc.want)
			}
		})
	}
}
