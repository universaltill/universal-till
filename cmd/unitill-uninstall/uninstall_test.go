package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
)

// fakeRunner records every exec call (into its own list AND a shared,
// ordered event log the test also feeds snapshot/verify events into, so
// ordering across exec and non-exec steps is assertable) and never touches
// a real apt-get/systemctl.
type fakeRunner struct {
	calls   []string
	errs    map[string]error
	missing map[string]bool
	events  *[]string
}

func (f *fakeRunner) Run(name string, args ...string) error {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, cmd)
	if f.events != nil {
		*f.events = append(*f.events, cmd)
	}
	if f.errs != nil {
		return f.errs[cmd]
	}
	return nil
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if f.missing[file] {
		return "", exec.ErrNotFound
	}
	return "/usr/bin/" + file, nil
}

func (f *fakeRunner) called(cmd string) bool {
	for _, c := range f.calls {
		if c == cmd {
			return true
		}
	}
	return false
}

const (
	stopCmd   = "systemctl stop unitill-pos.service"
	startCmd  = "systemctl start unitill-pos.service"
	removeCmd = "apt-get remove -y unitill-pos"
	purgeCmd  = "apt-get purge -y unitill-pos"
)

type testEnv struct {
	app    *app
	runner *fakeRunner
	events []string
	out    *bytes.Buffer
	errOut *bytes.Buffer
	cfg    *config.Config
	home   string
}

// newTestEnv builds an app wired to a real migrated SQLite DB in a temp
// dir, a fake runner, and a fake root check — everything else is the real
// production code path.
func newTestEnv(t *testing.T, args []string, stdin string) *testEnv {
	t.Helper()
	clearConfigEnv(t)
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	envFile := filepath.Join(dir, "pos.env")
	if err := os.WriteFile(envFile, []byte("UT_DATA_DIR="+dataDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServiceConfig(envFile, filepath.Join(dir, "unused-default"))
	if err != nil {
		t.Fatal(err)
	}
	// Seed a real database so Snapshot has something to copy.
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	opts, err := parseFlags(args)
	if err != nil {
		t.Fatalf("parseFlags(%v): %v", args, err)
	}

	env := &testEnv{
		out:    &bytes.Buffer{},
		errOut: &bytes.Buffer{},
		cfg:    cfg,
		home:   filepath.Join(dir, "home"),
	}
	if err := os.MkdirAll(env.home, 0o755); err != nil {
		t.Fatal(err)
	}
	env.runner = &fakeRunner{events: &env.events}
	env.app = &app{
		opts:   opts,
		cfg:    cfg,
		tr:     loadTranslator(filepath.Join("..", "..", "web", "locales"), "en"),
		stdout: env.out,
		stderr: env.errOut,
		stdin:  strings.NewReader(stdin),
		runner: env.runner,
		isRoot: func() bool { return true },
		snapshot: func(dbPath string) (string, error) {
			env.events = append(env.events, "snapshot")
			return openAndSnapshot(dbPath)
		},
		verify: func(path string) error {
			env.events = append(env.events, "verify")
			return verifyBackup(path)
		},
		chown:      func(string, int, int) error { return nil },
		sudoUser:   func() (sudoUser, bool) { return sudoUser{}, false },
		homeDir:    func() (string, error) { return env.home, nil },
		systemdDir: filepath.Join(dir, "systemd"),
		optDir:     filepath.Join(dir, "opt-unitill"),
	}
	return env
}

func (e *testEnv) eventIndex(t *testing.T, ev string) int {
	t.Helper()
	for i, got := range e.events {
		if got == ev {
			return i
		}
	}
	t.Fatalf("event %q never happened; events: %v", ev, e.events)
	return -1
}

func TestRunRefusesNonRoot(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	env.app.isRoot = func() bool { return false }
	if err := env.app.run(); err == nil {
		t.Fatal("non-root run must refuse")
	}
	if len(env.runner.calls) != 0 {
		t.Errorf("non-root run must execute nothing, got %v", env.runner.calls)
	}
}

func TestRunRefusesWithoutApt(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	env.runner.missing = map[string]bool{"apt-get": true}
	err := env.app.run()
	if err == nil {
		t.Fatal("missing apt-get must refuse")
	}
	if !strings.Contains(err.Error(), "uninstall-unitill.sh") {
		t.Errorf("refusal must point at the portable uninstall script, got: %v", err)
	}
	if len(env.runner.calls) != 0 {
		t.Errorf("must execute nothing, got %v", env.runner.calls)
	}
}

func TestRunRefusesWithoutDpkg(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	env.runner.missing = map[string]bool{"dpkg": true}
	if err := env.app.run(); err == nil {
		t.Fatal("missing dpkg must refuse")
	}
}

// --yes alone: backup to the invoking user's home, service stopped BEFORE
// the snapshot, verification BEFORE apt-get, and the safe keep-data
// `apt-get remove`.
func TestRunYesDefaultsBackupAndKeepData(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	stop := env.eventIndex(t, stopCmd)
	snap := env.eventIndex(t, "snapshot")
	verify := env.eventIndex(t, "verify")
	remove := env.eventIndex(t, removeCmd)
	if !(stop < snap && snap < verify && verify < remove) {
		t.Errorf("order must be stop < snapshot < verify < remove; events: %v", env.events)
	}
	if env.runner.called(purgeCmd) {
		t.Error("--yes alone must never purge")
	}
	// Backup landed in the fallback home dir and is non-empty.
	matches, err := filepath.Glob(filepath.Join(env.home, "unitill-pos-*.db"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one backup in home, got %v (%v)", matches, err)
	}
	if fi, err := os.Stat(matches[0]); err != nil || fi.Size() == 0 {
		t.Errorf("backup must be non-empty: %v %v", fi, err)
	}
	if !strings.Contains(env.out.String(), matches[0]) {
		t.Error("output must name the backup path")
	}
}

func TestRunBackupToOverride(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "nested", "backups")
	env := newTestEnv(t, []string{"--yes", "--backup-to", dest}, "")
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dest, "unitill-pos-*.db"))
	if len(matches) != 1 {
		t.Fatalf("want exactly one backup in --backup-to dir, got %v", matches)
	}
}

// A --backup-to under a directory this uninstall itself goes on to delete
// (a.optDir, or the configured DataDir) must be refused before anything
// happens — a "verified" backup that then gets deleted is worse than no
// backup at all. Review finding, ut-docs#1083.
func TestRunRefusesBackupToUnsafeDest(t *testing.T) {
	tests := []struct {
		name string
		dest func(env *testEnv) string
	}{
		{"nested under optDir", func(env *testEnv) string {
			return filepath.Join(env.app.optDir, "data", "backups")
		}},
		{"exactly cfg.DataDir", func(env *testEnv) string {
			return env.cfg.DataDir
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t, []string{"--yes"}, "")
			dest := tt.dest(env)
			env.app.opts.backupTo = dest
			if err := env.app.run(); err == nil {
				t.Fatal("must refuse an unsafe --backup-to")
			}
			if len(env.runner.calls) != 0 {
				t.Errorf("must refuse before touching anything (not even stop the service), calls: %v", env.runner.calls)
			}
		})
	}
}

func TestRunPurgeNoBackup(t *testing.T) {
	env := newTestEnv(t, []string{"--yes", "--purge-data", "--no-backup"}, "")
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !env.runner.called(purgeCmd) {
		t.Errorf("want %q, calls: %v", purgeCmd, env.runner.calls)
	}
	if env.runner.called(removeCmd) || env.runner.called(stopCmd) {
		t.Errorf("no-backup purge must not remove-keep or stop for backup: %v", env.runner.calls)
	}
	for _, ev := range env.events {
		if ev == "snapshot" {
			t.Error("--no-backup must not snapshot")
		}
	}
}

// Interactive: plain Enter accepts the backup default (yes), plain Enter
// at the data question keeps data.
func TestRunInteractiveEnterEnterIsBackupAndKeep(t *testing.T) {
	env := newTestEnv(t, nil, "\n\n")
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	env.eventIndex(t, "snapshot")
	if !env.runner.called(removeCmd) {
		t.Errorf("want keep-data remove, calls: %v", env.runner.calls)
	}
}

// Interactive: decline backup, type the exact DELETE word → purge.
func TestRunInteractiveDeclineBackupThenDelete(t *testing.T) {
	env := newTestEnv(t, nil, "n\nDELETE\n")
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, ev := range env.events {
		if ev == "snapshot" {
			t.Error("declined backup must not snapshot")
		}
	}
	if !env.runner.called(purgeCmd) {
		t.Errorf("exact DELETE must purge, calls: %v", env.runner.calls)
	}
}

// Interactive: a wrong confirmation word must fall back to keep-data.
func TestRunInteractiveWrongWordKeepsData(t *testing.T) {
	env := newTestEnv(t, nil, "n\ndelete\n")
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if env.runner.called(purgeCmd) {
		t.Error("lowercase 'delete' must NOT purge")
	}
	if !env.runner.called(removeCmd) {
		t.Errorf("mismatch must keep data (plain remove), calls: %v", env.runner.calls)
	}
}

// EOF on stdin (piped run without --yes) takes the safe defaults too.
func TestRunInteractiveEOFTakesDefaults(t *testing.T) {
	env := newTestEnv(t, nil, "")
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	env.eventIndex(t, "snapshot")
	if !env.runner.called(removeCmd) || env.runner.called(purgeCmd) {
		t.Errorf("EOF must mean backup+keep, calls: %v", env.runner.calls)
	}
}

// A failed verification aborts loudly BEFORE apt-get runs, and tries to
// bring the till back up.
func TestRunVerifyFailureAbortsBeforeRemoval(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	env.app.verify = func(string) error { return errors.New("integrity_check: rotten") }
	err := env.app.run()
	if err == nil {
		t.Fatal("verification failure must be a hard error")
	}
	if env.runner.called(removeCmd) || env.runner.called(purgeCmd) {
		t.Errorf("nothing may be removed after a failed verification: %v", env.runner.calls)
	}
	if !env.runner.called(startCmd) {
		t.Errorf("abort must try to restart the till, calls: %v", env.runner.calls)
	}
}

// A failed snapshot aborts the same way.
func TestRunSnapshotFailureAborts(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	env.app.snapshot = func(string) (string, error) { return "", errors.New("vacuum into: disk full") }
	if err := env.app.run(); err == nil {
		t.Fatal("snapshot failure must be a hard error")
	}
	if env.runner.called(removeCmd) || env.runner.called(purgeCmd) {
		t.Errorf("nothing may be removed after a failed snapshot: %v", env.runner.calls)
	}
	if !env.runner.called(startCmd) {
		t.Errorf("abort must try to restart the till, calls: %v", env.runner.calls)
	}
}

// A failed `systemctl stop` also aborts — a still-running writer makes the
// copy unsafe.
func TestRunStopFailureAborts(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	env.runner.errs = map[string]error{stopCmd: errors.New("unit jammed")}
	if err := env.app.run(); err == nil {
		t.Fatal("stop failure must be a hard error")
	}
	if env.runner.called(removeCmd) || env.runner.called(purgeCmd) {
		t.Errorf("nothing may be removed after a failed stop: %v", env.runner.calls)
	}
}

// A failed apt-get surfaces as an error (no silent success).
func TestRunAptFailureIsError(t *testing.T) {
	env := newTestEnv(t, []string{"--yes", "--no-backup"}, "")
	env.runner.errs = map[string]error{removeCmd: errors.New("dpkg lock held")}
	if err := env.app.run(); err == nil {
		t.Fatal("apt-get failure must be a hard error")
	}
	if env.runner.called(startCmd) {
		t.Errorf("no backup ran, so nothing stopped the service — must not restart it: %v", env.runner.calls)
	}
}

// A failed apt-get (a held dpkg lock is the realistic case) after a
// successful backup must not leave the shop's till silently stopped on top
// of nothing having been removed — review finding, ut-docs#1083.
func TestRunAptFailureAfterBackupRestartsService(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	env.runner.errs = map[string]error{removeCmd: errors.New("dpkg lock held: /var/lib/dpkg/lock-frontend")}
	if err := env.app.run(); err == nil {
		t.Fatal("apt-get failure must be a hard error")
	}
	if env.runner.called(removeCmd) == false {
		t.Fatalf("expected apt-get to have been attempted, calls: %v", env.runner.calls)
	}
	if !env.runner.called(startCmd) {
		t.Errorf("backup stopped the service — a failed apt-get must restart it, calls: %v", env.runner.calls)
	}
	stop := env.eventIndex(t, stopCmd)
	remove := env.eventIndex(t, removeCmd)
	start := env.eventIndex(t, startCmd)
	if !(stop < remove && remove < start) {
		t.Errorf("order must be stop < apt-get < restart; events: %v", env.runner.calls)
	}
}

// Running under sudo: the backup lands in $SUDO_USER's home and is chowned
// to them.
func TestRunSudoUserGetsOwnedBackup(t *testing.T) {
	env := newTestEnv(t, []string{"--yes"}, "")
	sudoHome := filepath.Join(t.TempDir(), "shopowner")
	if err := os.MkdirAll(sudoHome, 0o755); err != nil {
		t.Fatal(err)
	}
	env.app.sudoUser = func() (sudoUser, bool) {
		return sudoUser{Name: "shopowner", UID: 1234, GID: 5678, Home: sudoHome}, true
	}
	var chowned []string
	env.app.chown = func(path string, uid, gid int) error {
		if uid != 1234 || gid != 5678 {
			t.Errorf("chown uid/gid = %d/%d, want 1234/5678", uid, gid)
		}
		chowned = append(chowned, path)
		return nil
	}
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(sudoHome, "unitill-pos-*.db"))
	if len(matches) != 1 {
		t.Fatalf("want backup in sudo user's home, got %v", matches)
	}
	if len(chowned) != 1 || chowned[0] != matches[0] {
		t.Errorf("backup file must be chowned to the sudo user, chowned: %v", chowned)
	}
}

// Leftover reporting: names anything *unitill* under the systemd dir and
// the /opt/unitill root.
func TestLeftoverPaths(t *testing.T) {
	dir := t.TempDir()
	sysd := filepath.Join(dir, "systemd")
	opt := filepath.Join(dir, "opt-unitill")
	if got := leftoverPaths(sysd, opt); len(got) != 0 {
		t.Errorf("clean system: want none, got %v", got)
	}
	if err := os.MkdirAll(sysd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysd, "unitill-kiosk.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysd, "getty@.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opt, 0o755); err != nil {
		t.Fatal(err)
	}
	got := leftoverPaths(sysd, opt)
	want := []string{filepath.Join(sysd, "unitill-kiosk.service"), opt}
	if len(got) != 2 || got[0] != want[0] && got[1] != want[0] {
		t.Errorf("got %v, want both of %v", got, want)
	}
	for _, g := range got {
		if strings.Contains(g, "getty") {
			t.Errorf("non-unitill unit must not be reported: %v", got)
		}
	}
}

// After a keep-data run the leftover check reports the kept /opt tree.
func TestRunReportsLeftovers(t *testing.T) {
	env := newTestEnv(t, []string{"--yes", "--no-backup"}, "")
	if err := os.MkdirAll(env.app.optDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := env.app.run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(env.out.String(), env.app.optDir) {
		t.Errorf("output must list the kept %s; got:\n%s", env.app.optDir, env.out.String())
	}
}
