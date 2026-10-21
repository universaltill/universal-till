package languard

import (
	"net"
	"testing"
)

// ADR-0166 §2: the Host allow-list that defeats DNS rebinding on the wizard's
// cloud-pair route.
func TestHostAllowed(t *testing.T) {
	_, lan, _ := net.ParseCIDR("192.168.50.0/24")
	own := func() []net.IP { return []net.IP{net.ParseIP("10.1.2.3")} }
	for host, want := range map[string]bool{
		"localhost":          true,
		"LOCALHOST:8080":     true,
		"127.0.0.1":          true,
		"127.0.0.1:8080":     true,
		"[::1]:8080":         true,
		"::1":                true,
		"10.1.2.3:8080":      true, // own interface
		"192.168.50.7:8080":  true, // UT_UI_ALLOW_CIDRS
		"192.168.51.7":       false,
		"evil.example":       false,
		"evil.example:8080":  false,
		"localhost.evil.com": false,
		"":                   false,
		"[fe80::1%25eth0]":   false,
	} {
		if got := hostAllowed(host, []*net.IPNet{lan}, own); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}
