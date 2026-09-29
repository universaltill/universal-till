package logging

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
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
// back to (redacted) stderr alone once detached.
func TestStderrWritesToAttachedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.log")
	if err := AttachFile(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(DetachFile)
	fmt.Fprintln(Stderr(), "failed to start the till: token=abcdef123456")
	DetachFile()
	if Stderr() != (redactingWriter{w: os.Stderr}) {
		t.Fatal("Stderr after DetachFile must be redacted os.Stderr alone")
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

// ut-docs#3145: stdout and stderr (journald, a terminal, a service log) are
// log sinks as much as the file is, so every line reaching them is
// redacted — this package's logger on stdout, the stdlib "log" package and
// Stderr() on stderr — with no file attached (Init, android/ios), with one
// attached, and after DetachFile. Run in a subprocess so the real Init and
// the real os.Stdout/os.Stderr are what gets checked.
func TestConsoleSinksAreRedacted(t *testing.T) {
	const marker = "sink-marker"
	if mode := os.Getenv("UT_TEST_CONSOLE_SINK"); mode != "" {
		L() // Init, as app.go does first thing
		switch mode {
		case "attached", "detached":
			if err := AttachFile(os.Getenv("UT_TEST_CONSOLE_SINK_FILE")); err != nil {
				fmt.Fprintln(os.Stderr, "attach:", err)
				os.Exit(2)
			}
			if mode == "detached" {
				DetachFile()
			}
		}
		L().Warnf("%s %s", marker, sinkSecretLine)
		log.Printf("%s %s", marker, sinkSecretLine)
		fmt.Fprintln(Stderr(), marker, sinkSecretLine)
		DetachFile()
		os.Exit(0)
	}
	for _, mode := range []string{"nofile", "attached", "detached"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestConsoleSinksAreRedacted$")
			cmd.Env = append(os.Environ(),
				"UT_LOG_LEVEL=info", // a developer's exported level must not hide the marker line
				"UT_TEST_CONSOLE_SINK="+mode,
				"UT_TEST_CONSOLE_SINK_FILE="+filepath.Join(t.TempDir(), "till.log"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("child: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
			for name, out := range map[string]string{"stdout": stdout.String(), "stderr": stderr.String()} {
				want := 1 // this package's logger
				if name == "stderr" {
					want = 2 // stdlib log + Stderr()
				}
				if n := strings.Count(out, marker); n != want {
					t.Errorf("%s has %d marker lines, want %d: %q", name, n, want, out)
				}
				for _, s := range sinkSecrets {
					if strings.Contains(out, s) {
						t.Errorf("secret %q reached %s: %q", s, name, out)
					}
				}
			}
		})
	}
}
