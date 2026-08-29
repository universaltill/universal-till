// unitill-uninstall — friendly .deb uninstaller for Universal Till
// (ut-docs#1083). Wiring only: flags + real dependencies in, testable
// logic (uninstall.go) does the work — the same main-vs-logic split
// cmd/unitill-desktop uses.
//
// Installed to /usr/lib/unitill by the .deb (see .goreleaser.yaml nfpms —
// deliberately NOT /opt/unitill/bin, which is pos-writable and this must
// run as root) and symlinked onto PATH by packaging/scripts/postinstall.sh.
package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		// Flag errors happen before a locale is chosen — English + usage.
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: unitill-uninstall [--yes] [--no-backup] [--backup-to DIR] [--keep-data|--purge-data] [--lang CODE]")
		os.Exit(2)
	}

	envFile := os.Getenv("UT_ENV_FILE")
	if envFile == "" {
		envFile = debPosEnv
	}
	cfg, err := loadServiceConfig(envFile, debDataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve till config:", err)
		os.Exit(1)
	}

	lang := opts.lang
	if lang == "" {
		lang = primaryLang(cfg.Locales.Locale)
	}
	tr := loadTranslator(localeDir(), lang)

	a := &app{
		opts:       opts,
		cfg:        cfg,
		tr:         tr,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		stdin:      os.Stdin,
		runner:     &execRunner{stdout: os.Stdout, stderr: os.Stderr},
		isRoot:     func() bool { return os.Geteuid() == 0 },
		snapshot:   openAndSnapshot,
		verify:     verifyBackup,
		chown:      os.Chown,
		sudoUser:   lookupSudoUser,
		homeDir:    os.UserHomeDir,
		systemdDir: "/etc/systemd/system",
		optDir:     "/opt/unitill",
	}
	if err := a.run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// localeDirDefault is where the .deb installs web/locales — fixed,
// independent of where the unitill-uninstall binary itself lives. The
// binary is deliberately NOT under /opt/unitill (see .goreleaser.yaml's
// nfpms contents comment: it must run as root, and /opt/unitill is
// pos-writable for self-update, ut-docs#151 — co-locating locales-relative-
// to-executable, as an earlier draft of this did, silently broke once the
// binary moved to /usr/lib/unitill), so this cannot be derived from
// os.Executable() and is hardcoded to the one place web/ actually lives.
const localeDirDefault = "/opt/unitill/web/locales"

// localeDir resolves web/locales: UT_LOCALES_DIR override (tests, dev),
// else the installed path, else the working directory's web/locales for a
// source checkout (`go run ./cmd/unitill-uninstall`). loadTranslator
// degrades to key-echo output if none exist — never a crash.
func localeDir() string {
	if d := os.Getenv("UT_LOCALES_DIR"); d != "" {
		return d
	}
	if _, err := os.Stat(localeDirDefault); err == nil {
		return localeDirDefault
	}
	return filepath.Join("web", "locales")
}

// lookupSudoUser resolves $SUDO_USER so the backup file is handed to the
// operator who ran `sudo unitill-uninstall`, not left root-owned.
func lookupSudoUser() (sudoUser, bool) {
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return sudoUser{}, false
	}
	u, err := user.Lookup(name)
	if err != nil {
		return sudoUser{}, false
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return sudoUser{}, false
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return sudoUser{}, false
	}
	return sudoUser{Name: name, UID: uid, GID: gid, Home: u.HomeDir}, true
}
