package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ut-docs#6957: on macOS the till publishes through the OS responder
// (mDNSResponder), not a raw hashicorp/mdns socket — a raw responder is
// invisible to other devices there. These tests pin the seam on any OS.

type sysServer struct {
	fakeServer
	dead bool
}

func (s *sysServer) Exited() bool { return s.dead }

func TestAdvertiser_PrefersSystemResponder(t *testing.T) {
	a, starts, _ := newTestAdvertiser(t, true)
	var gotID string
	var gotPort int
	var gotTXT []string
	sys := &sysServer{}
	a.startSystem = func(id string, port int, txt []string) (mdnsServer, error) {
		gotID, gotPort, gotTXT = id, port, txt
		return sys, nil
	}
	a.tick(context.Background())
	if *starts != 0 {
		t.Fatalf("hashicorp zone started %d times; the system responder must replace it", *starts)
	}
	if gotID == "" || gotPort != 8080 {
		t.Fatalf("system responder got id=%q port=%d, want a till id and 8080", gotID, gotPort)
	}
	if !strings.Contains(strings.Join(gotTXT, ","), "id="+gotID) {
		t.Fatalf("TXT %v lacks id=%s", gotTXT, gotID)
	}
}

func TestAdvertiser_SystemResponderFailureFallsBackToHashicorp(t *testing.T) {
	a, starts, _ := newTestAdvertiser(t, true)
	a.startSystem = func(string, int, []string) (mdnsServer, error) { return nil, errors.New("no dns-sd") }
	a.tick(context.Background())
	if *starts != 1 {
		t.Fatalf("fallback zone started %d times, want 1", *starts)
	}
}

func TestAdvertiser_RestartsWhenSystemResponderExits(t *testing.T) {
	a, _, _ := newTestAdvertiser(t, true)
	n := 0
	first := &sysServer{}
	a.startSystem = func(string, int, []string) (mdnsServer, error) {
		n++
		if n == 1 {
			return first, nil
		}
		return &sysServer{}, nil
	}
	a.tick(context.Background())
	first.dead = true
	a.tick(context.Background())
	if n != 2 {
		t.Fatalf("system responder started %d times, want 2 (restart after exit)", n)
	}
}

func TestDNSSDArgs(t *testing.T) {
	got := dnssdArgs("abc", 8080, []string{"v=1", "name=Mac till", "id=abc"})
	want := []string{"-R", "abc", "_unitill-sync._tcp", "local", "8080", "v=1", "name=Mac till", "id=abc"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestIsOurDNSSD(t *testing.T) {
	ours := "/usr/bin/dns-sd -R oldid _unitill-sync._tcp local 8080 v=1 id=oldid"
	if !isOurDNSSD(ours) {
		t.Fatal("our own registration (even of an older till id) must be reapable")
	}
	for _, other := range []string{
		"/usr/bin/dns-sd -B _unitill-sync._tcp local",
		"/usr/bin/dns-sd -R x _ipp._tcp local 631",
		"/usr/bin/vim dns-sd.go",
		"",
	} {
		if isOurDNSSD(other) {
			t.Fatalf("%q must never be reaped (pid reuse)", other)
		}
	}
}
