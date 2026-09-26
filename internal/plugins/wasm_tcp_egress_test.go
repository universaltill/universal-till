package plugins

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"
)

// ut-docs#2891 security review M1: tcp_open dialled raw after only the
// tcp:<host>:<port> / tcp:* NAME check, so a tcp:* plugin could reach the
// till's own API on 127.0.0.1, cloud metadata on 169.254.169.254, or any LAN
// host through a name that resolves (or rebinds) there. tcp_open now goes
// through the same dial-time egress policy as http_request:
//   - tcp:* reaches PUBLIC addresses only;
//   - a non-public address needs the exact tcp:<host>:<port> grant;
//   - the till's own listen port is refused whatever the grant.

// withTCPEgress swaps the tcp_open dialer for one built on env for this test.
func withTCPEgress(t *testing.T, env *egressEnv) {
	t.Helper()
	prev := tcpEgressDialer
	tcpEgressDialer = env.dialer()
	t.Cleanup(func() { tcpEgressDialer = prev })
}

func echoFixture(t *testing.T) (string, int, func() int32) {
	t.Helper()
	host, port, accepts, _ := startTCPFixture(t, func(conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, 64)
		if n, err := conn.Read(buf); err == nil {
			_, _ = conn.Write(buf[:n])
		}
	})
	return host, port, accepts.Load
}

func TestHostTCPEgressPolicy(t *testing.T) {
	guest := buildTCPGuest(t)
	fxHost, fxPort, accepts := echoFixture(t)
	fxAddr := net.JoinHostPort(fxHost, strconv.Itoa(fxPort))
	_, tillPort, tillAccepts := echoFixture(t)

	env := &egressEnv{
		dns: map[string][]string{
			"terminal.example.com": {"93.184.216.34"},
			"rebind.example.com":   {"10.1.2.3"},
			"metadata.example.com": {"169.254.169.254"},
		},
		route: map[string]string{
			"93.184.216.34": fxAddr, "10.1.2.3": fxAddr, "169.254.169.254": fxAddr,
			"192.168.1.50": fxAddr, "0177.0.0.1": fxAddr,
		},
		tillPort: tillPort,
	}
	withTCPEgress(t, env)

	type tc struct {
		name   string
		perms  []string
		host   string
		port   int
		wantOK bool
		noHit  func() int32
	}
	cases := []tc{
		{name: "tcp:* reaches a public host", perms: []string{"tcp:*"}, host: "terminal.example.com", port: fxPort, wantOK: true},
		{name: "tcp:* refuses a loopback literal", perms: []string{"tcp:*"}, host: fxHost, port: fxPort, noHit: accepts},
		{name: "tcp:* refuses ::1", perms: []string{"tcp:*"}, host: "::1", port: fxPort, noHit: accepts},
		{name: "tcp:* refuses an IPv4-mapped loopback", perms: []string{"tcp:*"}, host: "::ffff:127.0.0.1", port: fxPort, noHit: accepts},
		{name: "tcp:* refuses cloud metadata", perms: []string{"tcp:*"}, host: "169.254.169.254", port: fxPort, noHit: accepts},
		{name: "tcp:* refuses a name resolving to metadata", perms: []string{"tcp:*"}, host: "metadata.example.com", port: fxPort, noHit: accepts},
		{name: "tcp:* refuses a LAN literal", perms: []string{"tcp:*"}, host: "192.168.1.50", port: fxPort, noHit: accepts},
		{name: "tcp:* refuses DNS rebinding to a LAN IP", perms: []string{"tcp:*"}, host: "rebind.example.com", port: fxPort, noHit: accepts},
		{name: "exact LAN grant allowed", perms: []string{fmt.Sprintf("tcp:192.168.1.50:%d", fxPort)}, host: "192.168.1.50", port: fxPort, wantOK: true},
		{name: "exact loopback grant allowed (local bridge)", perms: []string{fmt.Sprintf("tcp:%s:%d", fxHost, fxPort)}, host: fxHost, port: fxPort, wantOK: true},
		{name: "exact grant for the till's own port refused", perms: []string{fmt.Sprintf("tcp:%s:%d", fxHost, tillPort)}, host: fxHost, port: tillPort, noHit: tillAccepts},
		{name: "tcp:* + exact grant still refuses the till's own port", perms: []string{"tcp:*", fmt.Sprintf("tcp:localhost:%d", tillPort)}, host: "localhost", port: tillPort, noHit: tillAccepts},
	}
	env.dns["localhost"] = []string{"127.0.0.1"}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := hostfnTestDB(t)
			pluginID := fmt.Sprintf("com.test.tcpegress%d", i)
			seedPlugin(t, d, pluginID)
			grantPerm(t, d, pluginID, "storage")
			for _, p := range c.perms {
				grantPerm(t, d, pluginID, p)
			}
			w := newTCPTestRuntime(t, guest, pluginID)
			var before int32
			if c.noHit != nil {
				before = c.noHit()
			}
			res := runTCPGuest(t, w, d, pluginID, map[string]any{
				"mode": "roundtrip", "host": c.host, "port": c.port,
				"connect_timeout_ms": 2000, "read_timeout_ms": 2000, "send": "ping",
			})
			if c.wantOK {
				if res["open_code"] != float64(0) || res["read_data"] != "ping" {
					t.Fatalf("open_code = %v read_data = %v, want an echoed round trip", res["open_code"], res["read_data"])
				}
				return
			}
			if res["open_code"] != float64(hostErrDenied) {
				t.Fatalf("open_code = %v, want %d (denied)", res["open_code"], hostErrDenied)
			}
			if c.noHit != nil {
				time.Sleep(50 * time.Millisecond) // let a (wrong) accept land
				if c.noHit() != before {
					t.Fatal("refused tcp_open still reached the target")
				}
			}
		})
	}
}

// An open socket to a LAN device authorised by the EXACT grant must not
// survive that grant being revoked just because the plugin also holds tcp:*
// — tcp:* never covers a non-public address, so the per-call re-check has to
// remember what the handle is connected to.
func TestHostTCPRevokeExactKeepsWildcardOffLAN(t *testing.T) {
	guest := buildTCPGuest(t)
	fxHost, fxPort, _, conns := startTCPFixture(t, nil)
	env := &egressEnv{
		dns:   map[string][]string{},
		route: map[string]string{"192.168.1.50": net.JoinHostPort(fxHost, strconv.Itoa(fxPort))},
	}
	withTCPEgress(t, env)

	d := hostfnTestDB(t)
	const pluginID = "com.test.tcpdowngrade"
	exact := fmt.Sprintf("tcp:192.168.1.50:%d", fxPort)
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")
	grantPerm(t, d, pluginID, "tcp:*")
	grantPerm(t, d, pluginID, exact)

	w := newTCPTestRuntime(t, guest, pluginID)
	res := runTCPGuest(t, w, d, pluginID, map[string]any{
		"mode": "openonly", "host": "192.168.1.50", "port": fxPort, "connect_timeout_ms": 2000,
	})
	if res["open_code"] != float64(0) {
		t.Fatalf("open_code = %v, want 0", res["open_code"])
	}
	select {
	case c := <-conns:
		defer c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("fixture never received the connection")
	}
	revokePerm(t, d, pluginID, exact)
	ctx := withHostState(context.Background(), &hostState{pluginID: pluginID, db: d})
	if got := hostTCPWrite(ctx, nil, 0, 0, 0); got != hostErrDenied {
		t.Errorf("tcp_write on a LAN socket after the exact grant was revoked (tcp:* still held) = %d, want %d", got, hostErrDenied)
	}
	if got := hostTCPRead(ctx, nil, 0, 0, 1, 100); got != hostErrDenied {
		t.Errorf("tcp_read on a LAN socket after the exact grant was revoked (tcp:* still held) = %d, want %d", got, hostErrDenied)
	}
}
