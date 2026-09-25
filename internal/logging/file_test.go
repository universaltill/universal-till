package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// UT_LOG_FILE decides whether (and where) the till writes its log file.
// Default: on for desktop/server OSes, off on Android/iOS (logcat / the OS
// log already hold it, and the app sandbox is not user-browsable).
func TestResolveFile(t *testing.T) {
	data := filepath.Join("d", "UniversalTill")
	def := filepath.Join(data, "logs", "till.log")
	custom := filepath.Join(t.TempDir(), "unitill", "pos.log")
	cases := []struct {
		env, goos string
		want      string
		on        bool
	}{
		{"", "windows", def, true},
		{"", "darwin", def, true},
		{"", "linux", def, true},
		{"", "android", "", false},
		{"", "ios", "", false},
		{"0", "windows", "", false},
		{"off", "linux", "", false},
		{"false", "darwin", "", false},
		{"1", "android", def, true},
		{"on", "windows", def, true},
		{custom, "linux", custom, true},
	}
	for _, c := range cases {
		got, on := ResolveFile(c.env, data, c.goos, "till.log")
		if got != c.want || on != c.on {
			t.Errorf("ResolveFile(%q, %s) = (%q, %v), want (%q, %v)", c.env, c.goos, got, on, c.want, c.on)
		}
	}
}

// ut-docs#2720 review: a relative UT_LOG_FILE path is made absolute, so the
// log never lands in whatever the process's working directory happens to be
// (a Windows shortcut's "Start in", the service manager's /).
func TestResolveFileMakesCustomPathAbsolute(t *testing.T) {
	rel := filepath.Join("logs", "pos.log")
	got, on := ResolveFile(rel, "data", "linux", "till.log")
	if !on {
		t.Fatal("custom path must turn the file on")
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("ResolveFile(%q) = %q, want an absolute path", rel, got)
	}
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ResolveFile(%q) = %q, want %q", rel, got, want)
	}
}

// The desktop shell's plain fmt.Fprint(logging.Stderr(), …) messages land
// in the attached file too — timestamped and redacted — and Stderr falls
// back to plain stderr once detached.
func TestStderrWritesToAttachedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.log")
	if err := AttachFile(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(DetachFile)
	fmt.Fprintln(Stderr(), "failed to start the till: token=abcdef123456")
	DetachFile()
	if Stderr() != os.Stderr {
		t.Fatal("Stderr after DetachFile must be plain os.Stderr")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	if !strings.Contains(line, "failed to start the till: token="+redactedMark) || strings.Contains(line, "abcdef123456") {
		t.Fatalf("shell message not redacted into file: %q", line)
	}
	if _, err := time.Parse(time.RFC3339, strings.SplitN(line, " ", 2)[0]); err != nil {
		t.Fatalf("shell message not timestamped: %q", line)
	}
}
