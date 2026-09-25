package config

import "testing"

// ADR-0113 §1.1 (ut-docs#2687): UT_DEMO and UT_DEMO_TOKEN are read once,
// here, into Config.Demo / Config.DemoToken. Nothing else reads them.
func TestInitDemoFields(t *testing.T) {
	cases := []struct {
		name      string
		demo      string // "" = unset
		token     string
		wantDemo  bool
		wantToken string
		wantErr   bool
	}{
		{name: "unset", wantDemo: false},
		{name: "one", demo: "1", token: "tok", wantDemo: true, wantToken: "tok"},
		{name: "true", demo: "true", wantDemo: true},
		{name: "zero", demo: "0", token: "tok", wantDemo: false, wantToken: "tok"},
		{name: "false", demo: "false", wantDemo: false},
		// An unparseable value is a refusal to start, never a guess either way.
		{name: "garbage", demo: "yes please", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetForTest(t, configEnvKeys)
			t.Setenv("UT_DATA_DIR", t.TempDir())
			if tc.demo != "" {
				t.Setenv("UT_DEMO", tc.demo)
			}
			if tc.token != "" {
				t.Setenv("UT_DEMO_TOKEN", tc.token)
			}
			cfg, err := Init()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Init with UT_DEMO=%q: want error, got nil", tc.demo)
				}
				return
			}
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if cfg.Demo != tc.wantDemo {
				t.Errorf("Demo = %v, want %v", cfg.Demo, tc.wantDemo)
			}
			if cfg.DemoToken != tc.wantToken {
				t.Errorf("DemoToken = %q, want %q", cfg.DemoToken, tc.wantToken)
			}
		})
	}
}

// UT_AUTH is read once, here, into Config.AuthDisabled (same helper as
// ever, auth.Disabled) so the demo start gate and pages.Init agree.
func TestInitAuthDisabled(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{{"", false}, {"on", false}, {"off", true}, {" OFF ", true}} {
		t.Run("UT_AUTH="+tc.env, func(t *testing.T) {
			unsetForTest(t, configEnvKeys)
			t.Setenv("UT_DATA_DIR", t.TempDir())
			t.Setenv("UT_AUTH", tc.env)
			cfg, err := Init()
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if cfg.AuthDisabled != tc.want {
				t.Errorf("AuthDisabled = %v, want %v", cfg.AuthDisabled, tc.want)
			}
		})
	}
}
