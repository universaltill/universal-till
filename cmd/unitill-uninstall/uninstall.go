// unitill-uninstall's flow (ut-docs#1083): a friendly, localized front
// door over `apt-get remove`/`purge` for the .deb install. Backup FIRST
// (default yes, written somewhere the uninstall can't delete, verified
// before anything is destroyed), THEN the separate keep-data/remove-data
// question, THEN apt-get does the actual removal — plain `apt remove` /
// `apt remove --purge` keep working unmodified on their own.
//
// Everything with a side effect is injected (runner, isRoot, snapshot,
// verify, chown, sudoUser, homeDir, the leftover-scan roots) so
// uninstall_test.go drives the whole flow without root, apt, or systemd.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/db"
)

const (
	serviceName = "unitill-pos.service"
	packageName = "unitill-pos"
)

// sudoUser identifies the invoking (pre-sudo) operator, so the backup file
// ends up readable by the shop owner instead of root-locked.
type sudoUser struct {
	Name     string
	UID, GID int
	Home     string
}

type app struct {
	opts   *options
	cfg    *config.Config
	tr     *translator
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader

	runner   Runner
	isRoot   func() bool
	snapshot func(dbPath string) (string, error)
	verify   func(path string) error
	chown    func(path string, uid, gid int) error
	sudoUser func() (sudoUser, bool)
	homeDir  func() (string, error)

	// Leftover-scan roots (the real /etc/systemd/system and /opt/unitill
	// in main; temp dirs in tests).
	systemdDir string
	optDir     string
}

// openAndSnapshot reuses the product's own backup mechanism unmodified:
// internal/db.Open (same-release binary, so its migration pass is a no-op
// against an up-to-date DB) + internal/db.Snapshot (VACUUM INTO — a safe,
// checkpointed online copy once the service writer is stopped).
func openAndSnapshot(dbPath string) (string, error) {
	database, err := db.Open(dbPath)
	if err != nil {
		return "", err
	}
	defer database.Close()
	return db.Snapshot(database.DB, dbPath)
}

func (a *app) run() error {
	if !a.isRoot() {
		return errors.New(a.tr.T("uninstall.err_root"))
	}
	// Not a .deb box (portable tar.gz install): refuse and point at the
	// bundled uninstall-unitill.sh instead — never guess at removal.
	if _, err := a.runner.LookPath("apt-get"); err != nil {
		return errors.New(a.tr.T("uninstall.err_no_apt"))
	}
	if _, err := a.runner.LookPath("dpkg"); err != nil {
		return errors.New(a.tr.T("uninstall.err_no_apt"))
	}

	fmt.Fprintln(a.stdout, a.tr.T("uninstall.title"))
	in := bufio.NewReader(a.stdin)

	// 1. Backup first — default YES; plain Enter (or --yes) accepts it.
	doBackup := true
	switch {
	case a.opts.noBackup:
		doBackup = false
	case a.opts.yes:
		doBackup = true
	default:
		doBackup = promptYesDefault(in, a.stdout, a.tr.T("uninstall.prompt_backup"))
	}

	if doBackup {
		if err := a.makeVerifiedBackup(); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(a.stdout, a.tr.T("uninstall.backup_skipped"))
	}

	// 2. Then, separately: keep or remove shop data. Keeping is the plain
	// `apt remove` behaviour; removing needs the typed DELETE word (or the
	// explicit --purge-data flag in scripted use).
	purge, explicit := a.opts.explicitDisposition()
	if !explicit {
		fmt.Fprint(a.stdout, a.tr.T("uninstall.prompt_data"))
		line, _ := in.ReadString('\n')
		fmt.Fprintln(a.stdout)
		purge = confirmWordMatches(line)
		if !purge && strings.TrimSpace(line) != "" {
			fmt.Fprintln(a.stdout, a.tr.T("uninstall.data_mismatch"))
		}
	}
	if purge {
		fmt.Fprintln(a.stdout, a.tr.T("uninstall.data_purge"))
	} else {
		fmt.Fprintln(a.stdout, a.tr.T("uninstall.data_kept"))
	}

	// 3. The removal itself is apt's, not ours.
	action := "remove"
	if purge {
		action = "purge"
	}
	fmt.Fprintf(a.stdout, a.tr.T("uninstall.removing")+"\n", "apt-get "+action)
	if err := a.runner.Run("apt-get", action, "-y", packageName); err != nil {
		if doBackup {
			// makeVerifiedBackup already stopped the service and hasn't
			// restarted it — a failed apt-get (a held dpkg lock is the
			// realistic case) must not leave the shop's till silently
			// down on top of nothing having been removed. abort() does
			// exactly that: loud message, best-effort restart, error.
			return a.abort("uninstall.err_apt", err)
		}
		return fmt.Errorf(a.tr.T("uninstall.err_apt"), err)
	}

	// 4. Best-effort leftover check.
	if left := leftoverPaths(a.systemdDir, a.optDir); len(left) > 0 {
		fmt.Fprintln(a.stdout, a.tr.T("uninstall.leftover_found"))
		for _, p := range left {
			fmt.Fprintln(a.stdout, "  "+p)
		}
	} else {
		fmt.Fprintln(a.stdout, a.tr.T("uninstall.leftover_none"))
	}
	fmt.Fprintln(a.stdout, a.tr.T("uninstall.done"))
	return nil
}

// makeVerifiedBackup stops the service (so VACUUM INTO checkpoints a quiet
// WAL), snapshots via the product's own mechanism, copies the snapshot
// somewhere the uninstall can't delete, verifies THAT copy, and hands the
// file to the invoking user. Any failure aborts the whole uninstall before
// apt-get is ever reached, restarting the till.
func (a *app) makeVerifiedBackup() error {
	// Checked BEFORE anything happens (service still running): an
	// explicit --backup-to under a directory the uninstall itself goes on
	// to delete would make "verified" meaningless — the review's exact
	// finding. No abort()/restart needed here, nothing has touched the
	// service yet.
	if a.opts.backupTo != "" {
		if root, unsafe := a.unsafeBackupRoot(a.opts.backupTo); unsafe {
			return fmt.Errorf(a.tr.T("uninstall.err_unsafe_backup_dest"), a.opts.backupTo, root)
		}
	}
	fmt.Fprintln(a.stdout, a.tr.T("uninstall.stopping_service"))
	if err := a.runner.Run("systemctl", "stop", serviceName); err != nil {
		return a.abort("uninstall.err_backup", err)
	}
	fmt.Fprintln(a.stdout, a.tr.T("uninstall.backup_creating"))
	snap, err := a.snapshot(a.cfg.DBPath)
	if err != nil {
		return a.abort("uninstall.err_backup", err)
	}
	destDir, owner, hasOwner, err := a.backupDest()
	if err != nil {
		return a.abort("uninstall.err_backup", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return a.abort("uninstall.err_backup", err)
	}
	dest := filepath.Join(destDir, filepath.Base(snap))
	if err := copyFile(snap, dest); err != nil {
		return a.abort("uninstall.err_backup", err)
	}
	if err := a.verify(dest); err != nil {
		return a.abort("uninstall.err_verify", err)
	}
	if hasOwner {
		if err := a.chown(dest, owner.UID, owner.GID); err != nil {
			return a.abort("uninstall.err_backup", err)
		}
	}
	fmt.Fprintf(a.stdout, a.tr.T("uninstall.backup_saved")+"\n", dest)
	return nil
}

// abort is the loud, nothing-was-removed failure path: it restarts the
// till (best-effort — the box should not be left with the POS down over a
// failed backup) and returns a localized error.
func (a *app) abort(key string, cause error) error {
	fmt.Fprintln(a.stderr, a.tr.T("uninstall.abort_nothing_removed"))
	_ = a.runner.Run("systemctl", "start", serviceName)
	return fmt.Errorf(a.tr.T(key), cause)
}

// unsafeBackupRoot reports whether dir sits inside a directory tree this
// uninstall goes on to delete: /var/lib/unitill and /etc/unitill on
// apt-get purge (postremove.sh), a.optDir (== /opt/unitill in production,
// which nfpm's own contents get removed from on ANY remove/purge, plus
// its data/ subtree specifically on purge), and a.cfg.DataDir — included
// separately since an admin's UT_DATA_DIR override may point somewhere
// other than optDir/data but still lives on the same doomed path.
func (a *app) unsafeBackupRoot(dir string) (root string, unsafe bool) {
	roots := []string{a.optDir, "/var/lib/unitill", "/etc/unitill", a.cfg.DataDir}
	dir = filepath.Clean(dir)
	for _, r := range roots {
		if r == "" {
			continue
		}
		r = filepath.Clean(r)
		if dir == r || strings.HasPrefix(dir, r+string(filepath.Separator)) {
			return r, true
		}
	}
	return "", false
}

// backupDest resolves where the backup file goes: --backup-to wins, then
// the sudo invoker's home (with ownership handed to them), then the
// current user's home.
func (a *app) backupDest() (dir string, owner sudoUser, hasOwner bool, err error) {
	owner, hasOwner = a.sudoUser()
	if a.opts.backupTo != "" {
		return a.opts.backupTo, owner, hasOwner, nil
	}
	if hasOwner && owner.Home != "" {
		return owner.Home, owner, true, nil
	}
	home, err := a.homeDir()
	return home, owner, hasOwner, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	// The whole point of this file is surviving what happens next — make
	// sure it reached the disk before anything destructive runs.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// promptYesDefault asks a yes/no question whose default (plain Enter, or
// EOF on a piped stdin) is YES; only an explicit n/no declines.
func promptYesDefault(in *bufio.Reader, w io.Writer, prompt string) bool {
	fmt.Fprint(w, prompt)
	line, _ := in.ReadString('\n')
	fmt.Fprintln(w)
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "n", "no":
		return false
	}
	return true
}

// leftoverPaths is the post-removal best-effort check: anything named
// *unitill* under the systemd unit dir, plus the /opt/unitill root itself
// if it still exists (expected after a keep-data remove — the data stays).
func leftoverPaths(systemdDir, optDir string) []string {
	var out []string
	if entries, err := os.ReadDir(systemdDir); err == nil {
		for _, e := range entries {
			if strings.Contains(e.Name(), "unitill") {
				out = append(out, filepath.Join(systemdDir, e.Name()))
			}
		}
	}
	if _, err := os.Stat(optDir); err == nil {
		out = append(out, optDir)
	}
	return out
}
