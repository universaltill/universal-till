package logging

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// EnvFileNone is StartupInfo.EnvFile when no pos.env was loaded.
const EnvFileNone = "none (process environment and compiled defaults)"

// StartupInfo is what a support person needs first from a till's log
// (ut-docs#2720): which build, where its data is, which pos.env it
// actually read, which cloud it talks to, and whether it is enrolled.
type StartupInfo struct {
	Version     string
	GOOS        string
	GOARCH      string
	DataDir     string
	EnvFile     string // absolute path of the pos.env loaded, "" = none
	EndpointURL string // full URL; only the host is ever printed
	Enrolled    bool
	StoreID     string // only the last 4 characters are ever printed
	Role        string // primary | replica | backoffice
	LogFile     string // "" = no log file
}

// Line renders the single "startup:" log line.
func (s StartupInfo) Line() string {
	envFile := s.EnvFile
	if envFile == "" {
		envFile = EnvFileNone
	}
	enrolled := "no"
	if s.Enrolled {
		enrolled = "yes"
	}
	role := s.Role
	if role == "" {
		role = "unknown"
	}
	logFile := s.LogFile
	if logFile == "" {
		logFile = "off"
	}
	return fmt.Sprintf("startup: version=%s os=%s/%s data_dir=%q pos_env=%q cloud_host=%s enrolled=%s store=%s role=%s log_file=%q",
		s.Version, s.GOOS, s.GOARCH, s.DataDir, envFile, EndpointHost(s.EndpointURL), enrolled, StoreSuffix(s.StoreID), role, logFile)
}

// EndpointHost is the host[:port] of a cloud endpoint URL — never its
// path, query or credentials. "none" when unset, "invalid" when unparsable.
func EndpointHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "none"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	return u.Host
}

// StoreSuffix is "…" + the last 4 characters of a store id — enough to tell
// two stores apart on a support call, not enough to be the id.
func StoreSuffix(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "none"
	}
	r := []rune(id)
	if len(r) > 4 {
		r = r[len(r)-4:]
	}
	return "…" + string(r)
}

var (
	startupMu sync.Mutex
	startup   StartupInfo
)

// RememberStartup records the boot-time info so the Settings diagnostics
// card can show the same facts (and which pos.env was loaded) later.
func RememberStartup(s StartupInfo) {
	startupMu.Lock()
	defer startupMu.Unlock()
	startup = s
}

// RememberedStartup returns what RememberStartup recorded.
func RememberedStartup() StartupInfo {
	startupMu.Lock()
	defer startupMu.Unlock()
	return startup
}
