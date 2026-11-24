package discovery

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// System responder (ut-docs#6957). On macOS a hashicorp/mdns server holds a raw
// UDP socket on 5353 beside mDNSResponder; measured on a real Mac it is never
// visible to `dns-sd -B` nor, in practice, to an iPhone, so a Mac main till
// was never found by "Find on this network". macOS ships its own responder:
// registering through it (the `dns-sd -R` tool) publishes the very same
// _unitill-sync._tcp record through the OS, needs no entitlement and no
// multicast socket of ours, and is what every other Apple device speaks.
// Other platforms keep the hashicorp/mdns zone.

// systemStarter publishes id/port/txt through the OS responder.
type systemStarter func(id string, port int, txt []string) (mdnsServer, error)

// defaultSystemResponder is the OS responder for this platform, nil where the
// hashicorp/mdns zone is the publisher.
func defaultSystemResponder() systemStarter {
	if runtime.GOOS == "darwin" {
		return startDNSSD
	}
	return nil
}

// exitReporter is implemented by a server that can notice its publisher died;
// Advertiser.tick then starts a fresh one.
type exitReporter interface{ Exited() bool }

// dnssdArgs is the `dns-sd` command line that registers one instance.
func dnssdArgs(id string, port int, txt []string) []string {
	args := []string{"-R", id, ServiceName, "local", strconv.Itoa(port)}
	return append(args, txt...)
}

const dnssdBin = "/usr/bin/dns-sd"

// dnssdPidFile remembers the child so a crashed till's orphan is reaped by
// the next start instead of advertising a dead till for ever.
func dnssdPidFile() string { return paths.Data("dns-sd-advertise.pid") }

type dnssdServer struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	done bool
}

func (s *dnssdServer) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

func (s *dnssdServer) Shutdown() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill() // our own child, by handle — never by name
	}
	_ = os.Remove(dnssdPidFile())
	return nil
}

func startDNSSD(id string, port int, txt []string) (mdnsServer, error) {
	reapOrphanDNSSD()
	cmd := exec.Command(dnssdBin, dnssdArgs(id, port, txt)...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("dns-sd: %w", err)
	}
	s := &dnssdServer{cmd: cmd}
	go func() {
		defer logging.RecoverAndLog("discovery.dnssd")
		_ = cmd.Wait()
		s.mu.Lock()
		s.done = true
		s.mu.Unlock()
	}()
	// A dns-sd that dies at once (bad arguments) must not count as started.
	time.Sleep(300 * time.Millisecond)
	if s.Exited() {
		return nil, fmt.Errorf("dns-sd exited immediately")
	}
	if err := os.MkdirAll(paths.DataDir(), 0o755); err == nil {
		_ = os.WriteFile(dnssdPidFile(), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	}
	return s, nil
}

// isOurDNSSD reports whether a ps command line is a dns-sd registering the
// till service. The instance id is deliberately not compared: a till whose id
// changed since the crash must still reap its old orphan (a stale pid is
// still excluded, because the line must be our registration of ServiceName).
func isOurDNSSD(cmdline string) bool {
	return strings.Contains(cmdline, dnssdBin) && strings.Contains(cmdline, " -R ") && strings.Contains(cmdline, ServiceName)
}

// reapOrphanDNSSD kills the dns-sd a crashed previous run left behind: only
// the exact pid recorded in the pid file, and only if it still is our
// dns-sd registration of the till service.
func reapOrphanDNSSD() {
	raw, err := os.ReadFile(dnssdPidFile())
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return
	}
	out, err := exec.Command("/bin/ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return
	}
	if !isOurDNSSD(string(out)) {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
