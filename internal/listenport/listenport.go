// Package listenport keeps a till's LAN listening port stable across
// launches (ut-docs#2722).
//
// A replica stores its main till's address as a fixed host:port at pairing
// (sync.primary_url). The Android shell used to ask the OS for an ephemeral
// port on every launch (mobile.freePort), so each restart of an Android main
// till moved it to a new random port and stranded every paired replica — the
// incident behind #2722 (37673 → 34029, 41747 → 34029). The port a till
// successfully served on is now persisted and preferred next time; a
// different port is used only when that one is genuinely busy, and the new
// one is then persisted in turn so it doesn't re-randomise either.
//
// The value lives in a small file in the data dir, not the settings table:
// the port has to be chosen before the database is opened (mobile.Start
// sets UT_LISTEN_ADDR before app.Run), and a settings row would also travel
// to replicas in the join snapshot and the admin bundle, where it means
// nothing.
package listenport

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultPort is the till's usual port — config.Init's own UT_LISTEN_ADDR
// default (":8080"), so an Android main till and a Linux/Windows one end up
// on the same well-known port unless it is taken.
const DefaultPort = 8080

// FileName is the file in the data dir holding the last successfully
// served port.
const FileName = "listen-port"

// fallbackSpan is how many ports above the preferred one are tried before
// giving up and letting the OS choose — the same +20 window
// internal/server.listenWithFallback uses.
const fallbackSpan = 20

// Saved returns the persisted port, or 0 when none is saved or the saved
// value is unusable (corrupt, privileged, out of range).
func Saved(dataDir string) int {
	raw, err := os.ReadFile(filepath.Join(dataDir, FileName))
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || !valid(p) {
		return 0
	}
	return p
}

// Choose returns the port to listen on: the saved one (or defaultPort when
// nothing is saved) if bindable, else the first bindable port in the next
// fallbackSpan, else 0 — meaning "let the OS pick", the last resort.
// bindable is injected so tests don't depend on the machine's real ports;
// production passes Bindable.
func Choose(dataDir string, defaultPort int, bindable func(port int) bool) int {
	preferred := Saved(dataDir)
	if preferred == 0 {
		preferred = defaultPort
	}
	if !valid(preferred) {
		return 0
	}
	for p := preferred; p <= preferred+fallbackSpan && p <= 65535; p++ {
		if bindable(p) {
			return p
		}
	}
	return 0
}

// Save persists port as the one to prefer next launch. Creates dataDir if
// needed (a fresh install's data dir may not exist yet) and writes via a
// temp file + rename so a crash mid-write never leaves a torn value.
func Save(dataDir string, port int) error {
	if !valid(port) {
		return errors.New("listenport: refusing to save an invalid port " + strconv.Itoa(port))
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	final := filepath.Join(dataDir, FileName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Bindable reports whether port can be bound on every interface right now.
// A probe, not a reservation — the caller binds it again moments later, the
// same (accepted) window mobile.freePort always had.
func Bindable(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// valid excludes 0 ("OS picks") and privileged ports an app can't bind on
// Android anyway.
func valid(p int) bool { return p >= 1024 && p <= 65535 }
