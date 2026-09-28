package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ut-docs#3092: a failed or finished Windows update leaves its attempt-*
// folder (the installer download) until the next update starts. The daily
// sweep removes ones older than the cutoff, keeps updater.log and anything
// else, and does nothing while an update is being handed to the installer.
func TestPruneStaleAttempts(t *testing.T) {
	root := t.TempDir()
	orig := windowsUpdateDir
	windowsUpdateDir = func() string { return root }
	t.Cleanup(func() { windowsUpdateDir = orig; windowsHandover.Store(false) })

	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	old := now.Add(-8 * 24 * time.Hour)
	mk := func(name string, dir bool, mod time.Time) string {
		p := filepath.Join(root, name)
		if dir {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p, "setup.exe"), []byte("12345"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, []byte("log"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk("attempt-111", true, old)
	fresh := mk("attempt-222", true, now.Add(-time.Hour))
	log := mk("updater.log", false, old)
	other := mk("something-else", true, old)

	windowsHandover.Store(true)
	if n, _, err := PruneStaleAttempts(now.Add(-7 * 24 * time.Hour)); err != nil || n != 0 {
		t.Fatalf("during hand-over: removed %d, err %v; want 0, nil", n, err)
	}
	windowsHandover.Store(false)

	n, freed, err := PruneStaleAttempts(now.Add(-7 * 24 * time.Hour))
	if err != nil || n != 1 || freed != 5 {
		t.Fatalf("PruneStaleAttempts = %d, %d, %v; want 1, 5, nil", n, freed, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale attempt kept")
	}
	for _, p := range []string{fresh, log, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed, want kept", p)
		}
	}
}

func TestPruneStaleAttempts_NoUpdateDir(t *testing.T) {
	orig := windowsUpdateDir
	windowsUpdateDir = func() string { return filepath.Join(t.TempDir(), "missing") }
	t.Cleanup(func() { windowsUpdateDir = orig })
	if n, _, err := PruneStaleAttempts(time.Now()); err != nil || n != 0 {
		t.Fatalf("PruneStaleAttempts = %d, %v; want 0, nil", n, err)
	}
}
