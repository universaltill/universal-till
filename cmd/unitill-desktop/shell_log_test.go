package main

import (
	"path/filepath"
	"testing"
)

// ut-docs#2720: the shell's own messages go to desktop.log in the same
// folder as the till's till.log — the folder the till resolves from the
// same UT_DATA_DIR (else the per-OS default) and UT_LOG_FILE the shell
// hands down to it.
func TestShellLogPath(t *testing.T) {
	def := filepath.Join("home", "UniversalTill")
	custom := filepath.Join("srv", "ut")
	logDir := filepath.Join(t.TempDir(), "var", "log", "unitill")
	relocated := filepath.Join(logDir, "till.log")
	cases := []struct {
		logEnv, dataEnv, goos string
		want                  string
		on                    bool
	}{
		{"", "", "windows", filepath.Join(def, "logs", "desktop.log"), true},
		{"", custom, "darwin", filepath.Join(custom, "logs", "desktop.log"), true},
		{"0", "", "linux", "", false},
		{relocated, "", "linux", filepath.Join(logDir, "desktop.log"), true},
	}
	for _, c := range cases {
		got, on := shellLogPath(c.logEnv, c.dataEnv, def, c.goos)
		if got != c.want || on != c.on {
			t.Errorf("shellLogPath(%q, %q, %s) = (%q, %v), want (%q, %v)", c.logEnv, c.dataEnv, c.goos, got, on, c.want, c.on)
		}
	}
}
