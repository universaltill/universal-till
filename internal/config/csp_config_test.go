package config

import "testing"

// ut-docs#2913: UT_CSP_REPORT_ONLY turns on the report-only
// Content-Security-Policy header and the /csp-report sink. Default off, and
// an unparseable value is off too — like UT_DEV_MODE and the other plain
// flags, a typo must never change what a production till sends.
func TestInitCSPReportOnly(t *testing.T) {
	cases := []struct {
		name string
		val  *string
		want bool
	}{
		{name: "unset", want: false},
		{name: "one", val: strp("1"), want: true},
		{name: "true", val: strp("true"), want: true},
		{name: "TRUE", val: strp("TRUE"), want: true},
		{name: "zero", val: strp("0"), want: false},
		{name: "false", val: strp("false"), want: false},
		{name: "empty", val: strp(""), want: false},
		{name: "garbage", val: strp("yes please"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetForTest(t, configEnvKeys)
			if tc.val != nil {
				t.Setenv("UT_CSP_REPORT_ONLY", *tc.val)
			}
			cfg, err := Init()
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if cfg.CSPReportOnly != tc.want {
				t.Errorf("CSPReportOnly = %v, want %v", cfg.CSPReportOnly, tc.want)
			}
		})
	}
}

func strp(s string) *string { return &s }
