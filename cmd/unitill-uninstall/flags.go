// Flag parsing for unitill-uninstall (ut-docs#1083) — kept apart from the
// flow logic (uninstall.go) the same way cmd/unitill-desktop splits its
// testable pieces (autostart.go, control.go) from main wiring.
package main

import (
	"errors"
	"flag"
	"io"
	"strings"
)

type options struct {
	yes       bool   // accept the default backup choice without prompting
	noBackup  bool   // skip the backup entirely
	backupTo  string // directory to write the backup into (default: invoker's home)
	keepData  bool   // scripted: plain `apt-get remove` (data survives)
	purgeData bool   // scripted: `apt-get purge` (data deleted) — the flag IS the consent
	lang      string // locale code for all output (default: pos.env's UT_DEFAULT_LOCALE, else en)
}

var (
	errConflictData   = errors.New("--keep-data and --purge-data cannot be combined")
	errConflictBackup = errors.New("--no-backup and --backup-to cannot be combined")
)

func parseFlags(args []string) (*options, error) {
	opts := &options{}
	fs := flag.NewFlagSet("unitill-uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // callers print errors themselves (localized)
	fs.BoolVar(&opts.yes, "yes", false, "non-interactive: accept the default backup choice")
	fs.BoolVar(&opts.noBackup, "no-backup", false, "skip the backup")
	fs.StringVar(&opts.backupTo, "backup-to", "", "directory to write the backup into")
	fs.BoolVar(&opts.keepData, "keep-data", false, "keep shop data (plain apt-get remove)")
	fs.BoolVar(&opts.purgeData, "purge-data", false, "also delete shop data (apt-get purge)")
	fs.StringVar(&opts.lang, "lang", "", "locale code for output (en, ar, fa, tr)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, errors.New("unexpected argument: " + fs.Arg(0))
	}
	if opts.keepData && opts.purgeData {
		return nil, errConflictData
	}
	if opts.noBackup && opts.backupTo != "" {
		return nil, errConflictBackup
	}
	return opts, nil
}

// explicitDisposition resolves the data question without a prompt where the
// flags already settle it: --keep-data/--purge-data are explicit, and --yes
// with neither defaults to keep-data — the safe, reversible choice (plain
// `apt remove` behaviour). Interactive runs (explicit=false) go through the
// typed-DELETE confirmation instead.
func (o *options) explicitDisposition() (purge, explicit bool) {
	switch {
	case o.purgeData:
		return true, true
	case o.keepData:
		return false, true
	case o.yes:
		return false, true
	}
	return false, false
}

// confirmWordMatches gates the destructive purge in interactive mode: the
// operator must type exactly DELETE (uppercase, case-sensitive; surrounding
// whitespace forgiven). Anything else keeps the data.
func confirmWordMatches(line string) bool {
	return strings.TrimSpace(line) == "DELETE"
}
