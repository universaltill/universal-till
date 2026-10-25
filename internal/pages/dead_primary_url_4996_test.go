package pages

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDeadPrimaryURLStaysDead pins ut-docs#4996: the "unreachable main till"
// must be an address no later httptest server can be handed, and dialling it
// must be refused, not answered.
func TestDeadPrimaryURLStaysDead(t *testing.T) {
	u, err := url.Parse(deadPrimaryURL())
	if err != nil {
		t.Fatalf("parse deadPrimaryURL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("deadPrimaryURL port %q: %v", u.Port(), err)
	}

	// Other servers coming up in the package must never land on the dead address.
	for i := 0; i < 20; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(srv.Close)
		su, _ := url.Parse(srv.URL)
		if su.Host == u.Host {
			t.Fatalf("httptest server %d got the dead address %s", i, u.Host)
		}
	}

	// Real proof: the port is below the ephemeral range httptest binds from.
	// Skipped silently where the range is not readable (non-Linux).
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
		if f := strings.Fields(string(b)); len(f) == 2 {
			if lo, err := strconv.Atoi(f[0]); err == nil && port >= lo {
				t.Errorf("dead port %d is inside the ephemeral range starting at %d", port, lo)
			}
		}
	}

	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("dialling %s succeeded; it must be refused", u.Host)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("dial %s: want ECONNREFUSED, got %v", u.Host, err)
	}
}
