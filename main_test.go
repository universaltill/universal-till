package main

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// TestMain lets a test run the real main() in a child process (the test
// binary re-executed with UT_TEST_RUN_MAIN=1), so the --version path is
// exercised exactly as selfupdate's smoke run invokes it.
func TestMain(m *testing.M) {
	if os.Getenv("UT_TEST_RUN_MAIN") == "1" {
		os.Args = append([]string{os.Args[0]}, strings.Fields(os.Getenv("UT_TEST_MAIN_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// ut-docs#2759: after swapping in a new binary, selfupdate runs
// `<new> --version` to prove it can start before restarting into it. That
// path must print the version and exit 0 fast, and touch nothing: no data
// dir, no database, no config, no listener.
func TestVersionFlagPrintsVersionAndTouchesNothing(t *testing.T) {
	root := t.TempDir()
	// Bounded: without the flag, main() boots the whole server and never
	// returns — the child is killed rather than hanging the suite.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"UT_TEST_RUN_MAIN=1", "UT_TEST_MAIN_ARGS=--version",
		"HOME="+root, "XDG_DATA_HOME="+filepath.Join(root, "xdg"), "LOCALAPPDATA="+root,
		"UT_DATA_DIR="+filepath.Join(root, "data"), "PORT=1",
	)
	start := time.Now()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("--version: %v (output %q)", err, out)
	}
	// Last line: package init may log to stdout first (selfupdate.smokeRun
	// reads it the same way).
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if got := lines[len(lines)-1]; got != buildinfo.Version {
		t.Fatalf("--version printed %q, want %q", got, buildinfo.Version)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("--version took %v — it must not boot the server", d)
	}
	var created []string
	_ = filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err == nil && p != root {
			created = append(created, p)
		}
		return nil
	})
	if len(created) > 0 {
		t.Fatalf("--version created files: %v", created)
	}
}
