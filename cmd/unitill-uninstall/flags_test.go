package main

import "testing"

// parseFlags is the whole scripted/remote contract (ut-docs#1083): every
// combination here is one a fleet admin may bake into an SSH one-liner, so
// each is pinned by a test rather than left to flag-package defaults.
func TestParseFlagsDefaults(t *testing.T) {
	opts, err := parseFlags(nil)
	if err != nil {
		t.Fatalf("parseFlags(nil): %v", err)
	}
	if opts.yes || opts.noBackup || opts.keepData || opts.purgeData {
		t.Fatalf("zero-arg parse must leave every bool flag false: %+v", opts)
	}
	if opts.backupTo != "" || opts.lang != "" {
		t.Fatalf("zero-arg parse must leave string flags empty: %+v", opts)
	}
}

func TestParseFlagsAll(t *testing.T) {
	opts, err := parseFlags([]string{"--yes", "--backup-to", "/tmp/x", "--purge-data", "--lang", "fa"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.yes || !opts.purgeData || opts.keepData || opts.noBackup {
		t.Fatalf("unexpected bools: %+v", opts)
	}
	if opts.backupTo != "/tmp/x" || opts.lang != "fa" {
		t.Fatalf("unexpected strings: %+v", opts)
	}
}

func TestParseFlagsUnknownFlagErrors(t *testing.T) {
	if _, err := parseFlags([]string{"--frobnicate"}); err == nil {
		t.Fatal("unknown flag must be an error, not silently ignored")
	}
}

func TestParseFlagsConflicts(t *testing.T) {
	if _, err := parseFlags([]string{"--keep-data", "--purge-data"}); err == nil {
		t.Fatal("--keep-data + --purge-data must conflict")
	}
	if _, err := parseFlags([]string{"--no-backup", "--backup-to", "/tmp/x"}); err == nil {
		t.Fatal("--no-backup + --backup-to must conflict")
	}
}

// --yes with neither data flag = the safe, reversible choice (keep data,
// plain `apt remove` behaviour) — the exact default ut-docs#1083 requires.
func TestExplicitDispositionYesDefaultsToKeep(t *testing.T) {
	opts, err := parseFlags([]string{"--yes"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	purge, explicit := opts.explicitDisposition()
	if !explicit {
		t.Fatal("--yes alone must settle disposition without prompting")
	}
	if purge {
		t.Fatal("--yes alone must default to keep-data, never purge")
	}
}

func TestExplicitDispositionFlags(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		wantPurge    bool
		wantExplicit bool
	}{
		{[]string{"--purge-data"}, true, true},
		{[]string{"--keep-data"}, false, true},
		{[]string{"--yes", "--purge-data"}, true, true},
		{[]string{"--yes", "--keep-data"}, false, true},
		{nil, false, false}, // interactive: prompt decides
	} {
		opts, err := parseFlags(tc.args)
		if err != nil {
			t.Fatalf("parseFlags(%v): %v", tc.args, err)
		}
		purge, explicit := opts.explicitDisposition()
		if purge != tc.wantPurge || explicit != tc.wantExplicit {
			t.Errorf("%v: got purge=%v explicit=%v, want purge=%v explicit=%v",
				tc.args, purge, explicit, tc.wantPurge, tc.wantExplicit)
		}
	}
}

// The DELETE confirmation word is exact and case-sensitive (documented in
// the prompt itself): a lowercase "delete", extra words, or anything else
// must fall back to the safe keep-data path.
func TestConfirmWordGate(t *testing.T) {
	for input, want := range map[string]bool{
		"DELETE":        true,
		"DELETE\n":      true,
		"  DELETE  \n":  true, // surrounding whitespace is forgiven
		"delete\n":      false,
		"Delete\n":      false,
		"DELETE ALL\n":  false,
		"\n":            false,
		"y\n":           false,
		"yes DELETE\n":  false,
		"DELETED\n":     false,
		"D E L E T E\n": false,
	} {
		if got := confirmWordMatches(input); got != want {
			t.Errorf("confirmWordMatches(%q) = %v, want %v", input, got, want)
		}
	}
}
