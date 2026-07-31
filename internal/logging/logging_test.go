package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The problems digest keeps only warn/error lines, newest first, capped.
func TestRecentKeepsWarnErrorNewestFirstCapped(t *testing.T) {
	l := L()
	l.Infof("just info %d", 1) // must not be remembered
	l.Warnf("warn one")
	l.Errorf("error two")

	got := Recent()
	if len(got) < 2 {
		t.Fatalf("recent = %d entries, want >= 2", len(got))
	}
	if got[0].Msg != "error two" || got[0].Level != "ERROR" {
		t.Fatalf("newest first: got %+v", got[0])
	}
	if got[1].Msg != "warn one" || got[1].Level != "WARN" {
		t.Fatalf("second: got %+v", got[1])
	}
	for _, p := range got {
		if strings.Contains(p.Msg, "just info") {
			t.Fatalf("info line remembered: %+v", p)
		}
	}

	for i := 0; i < recentCap+10; i++ {
		l.Warnf("flood %d", i)
	}
	if n := len(Recent()); n != recentCap {
		t.Fatalf("cap: %d entries, want %d", n, recentCap)
	}
}

func TestLevelString(t *testing.T) {
	cases := map[Level]string{
		Debug: "DEBUG", Info: "INFO", Warn: "WARN", Error: "ERROR", Fatal: "FATAL",
		Level(99): "UNKNOWN",
	}
	for lvl, want := range cases {
		if got := lvl.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", lvl, got, want)
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug": Debug, "info": Info, "warn": Warn, "warning": Warn,
		"error": Error, "fatal": Fatal,
		"  DEBUG  ": Debug, "Error": Error,
		"": Info, "verbose": Info, // unknown values default to Info
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// A level-filtered logger writes only at-or-above its level, and a nil
// *Logger is a safe no-op (early callers before Init).
func TestLogfLevelFilterAndNilSafety(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{level: Warn, log: log.New(&buf, "", 0)}
	l.Debugf("d %d", 1)
	l.Infof("i %d", 2)
	l.Warnf("w %d", 3)
	l.Errorf("e %d", 4)

	out := buf.String()
	if strings.Contains(out, "d 1") || strings.Contains(out, "i 2") {
		t.Fatalf("below-level lines logged: %q", out)
	}
	if !strings.Contains(out, "[WARN] w 3") || !strings.Contains(out, "[ERROR] e 4") {
		t.Fatalf("at-level lines missing: %q", out)
	}

	var nilL *Logger
	nilL.Debugf("no panic")
	nilL.Errorf("no panic")
}

// Fatalf must terminate the process with a non-zero exit after logging —
// callers (main.go) rely on it never returning. Verified in a subprocess.
func TestFatalfExitsProcess(t *testing.T) {
	if os.Getenv("UT_TEST_FATALF") == "1" {
		l := &Logger{level: Error, log: log.New(os.Stdout, "", 0)}
		l.Fatalf("fatal boom %d", 42)
		fmt.Println("unreachable: Fatalf returned")
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestFatalfExitsProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "UT_TEST_FATALF=1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("Fatalf must exit with code 1; err=%v out=%s", err, out)
	}
	if !strings.Contains(string(out), "[ERROR] fatal boom 42") {
		t.Fatalf("fatal message not logged before exit: %s", out)
	}
	if strings.Contains(string(out), "unreachable") {
		t.Fatalf("Fatalf returned instead of exiting: %s", out)
	}
}
