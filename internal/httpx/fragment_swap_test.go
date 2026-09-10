package httpx

import (
	"net/http/httptest"
	"testing"
)

// IsFragmentSwap backs the htmx-fragment-vs-full-page branch every one of
// /items' five section handlers now has (ut-docs#1950), mirroring
// renderHelpPage's original /help/{topic} check (ut-docs#433) — including
// the same HX-History-Restore-Request exclusion.
func TestIsFragmentSwap(t *testing.T) {
	cases := []struct {
		name       string
		hxRequest  string
		hxRestore  string
		wantResult bool
	}{
		{"plain browser GET", "", "", false},
		{"ordinary htmx navigation", "true", "", true},
		{"htmx history restore", "true", "true", false},
		{"history restore header alone (no HX-Request) is not a fragment swap", "", "true", false},
		// Header values are case-insensitive per the HTTP spec — htmx itself
		// always sends "true" lowercase, but a proxy/test harness normalizing
		// case must not flip the answer.
		{"case-insensitive HX-Request", "TRUE", "", true},
		{"case-insensitive HX-History-Restore-Request", "true", "TRUE", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/catalog", nil)
			if tc.hxRequest != "" {
				r.Header.Set("HX-Request", tc.hxRequest)
			}
			if tc.hxRestore != "" {
				r.Header.Set("HX-History-Restore-Request", tc.hxRestore)
			}
			if got := IsFragmentSwap(r); got != tc.wantResult {
				t.Errorf("IsFragmentSwap() = %v, want %v", got, tc.wantResult)
			}
		})
	}
}
