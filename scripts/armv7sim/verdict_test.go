package main

import (
	"os"
	"strings"
	"testing"
)

const wantSHA = "a07ae56188a587ad1def205eda378778f27be8c4e223545f3539ddac07c5bf1a"

func green(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/green.console.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestVerdictGreen(t *testing.T) {
	if f := Verdict(green(t), wantSHA); len(f) != 0 {
		t.Fatalf("green fixture failed: %v", f)
	}
}

func TestVerdictGreenWithCRLF(t *testing.T) {
	if f := Verdict(strings.ReplaceAll(green(t), "\n", "\r\n"), wantSHA); len(f) != 0 {
		t.Fatalf("serial CRLF must not matter: %v", f)
	}
}

func TestVerdictRed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string) string
		want   string // substring of one failure
	}{
		{"pristine survives the filter", func(s string) string {
			return strings.Replace(s, "exited=false code=-1 signaled=true signal=6", "exited=true code=0 signaled=false signal=0", 1)
		}, "pristine-filtered"},
		{"pristine killed by something else (r7 not statx)", func(s string) string {
			return strings.Replace(s, "r7      0x18d", "r7      0x15a", 1)
		}, "0x18d"},
		{"no sigcode=1", func(s string) string { return strings.Replace(s, "sigcode=1", "sigcode=0", 1) }, "sigcode=1"},
		{"no SIGSYS text", func(s string) string { return strings.Replace(s, "SIGSYS: bad system call", "fatal error", 1) }, "SIGSYS"},
		{"fork shows SIGSYS", func(s string) string {
			i := strings.Index(s, "UTSIM BEGIN fork-filtered")
			return s[:i+len("UTSIM BEGIN fork-filtered\n")] + "SIGSYS: bad system call\n" + s[i+len("UTSIM BEGIN fork-filtered\n"):]
		}, "fork-filtered"},
		{"fork without OK 200", func(s string) string {
			i := strings.LastIndex(s, "OK 200")
			return s[:i] + "NOPE" + s[i+6:]
		}, "fork-filtered"},
		{"control fails", func(s string) string {
			return strings.Replace(s, "UTSIM END control-pristine-nofilter exited=true code=0", "UTSIM END control-pristine-nofilter exited=true code=1", 1)
		}, "control-pristine-nofilter"},
		{"filter hash differs", func(s string) string { return strings.Replace(s, wantSHA, strings.Repeat("0", 64), 1) }, "filter_sha256"},
		{"wrong kernel", func(s string) string { return strings.Replace(s, "kernel=4.9.0-13-armmp-lpae", "kernel=6.1.0", 1) }, "kernel"},
		{"missing DONE", func(s string) string { return strings.Replace(s, "UTSIM DONE", "", 1) }, "DONE"},
		{"fatal", func(s string) string { return s + "UTSIM FATAL seed: boom\n" }, "FATAL"},
		{"timeout", func(s string) string { return strings.Replace(s, "signal=0 timeout=false", "signal=0 timeout=true", 1) }, "timeout"},
		{"duplicate scenario", func(s string) string {
			i := strings.Index(s, "UTSIM BEGIN fork-filtered")
			return s + s[i:]
		}, "exactly once"},
		{"missing READY", func(s string) string { return s[strings.Index(s, "UTSIM BEGIN"):] }, "READY"},
		{"empty log", func(string) string { return "" }, "READY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Verdict(c.mutate(green(t)), wantSHA)
			if len(f) == 0 {
				t.Fatal("mutated log still went green")
			}
			if !strings.Contains(strings.Join(f, "\n"), c.want) {
				t.Errorf("failures %q do not mention %q", f, c.want)
			}
		})
	}
}

// The sigcode=1 dump must belong to the SIGSYS: a SIGSEGV (SEGV_MAPERR is also
// code 1) next to a stray "SIGSYS" line elsewhere must not turn green.
func TestVerdictSigcodeMustFollowSIGSYS(t *testing.T) {
	s := strings.Replace(green(t), "SIGSYS: bad system call\nPC=", "SIGSEGV: segmentation violation\nPC=", 1)
	s = strings.Replace(s, "UTSIM BEGIN pristine-filtered\n", "UTSIM BEGIN pristine-filtered\nSIGSYS: bad system call\n", 1)
	if f := Verdict(s, wantSHA); len(f) == 0 {
		t.Fatal("a sigcode=1 dump that is not the SIGSYS one went green")
	}
}

// The first dump after the sigcode=1 line is the one that matters: a later
// thread dump showing 0x18d must not rescue a first dump that is not statx.
func TestVerdictOnlyFirstDumpCounts(t *testing.T) {
	s := strings.Replace(green(t), "r7      0x18d", "r7      0x15a", 1)
	s = strings.Replace(s, "r7      0x15a\nUTSIM END", "r7      0x15a\nr7      0x18d\nUTSIM END", 1)
	if f := Verdict(s, wantSHA); len(f) == 0 {
		t.Fatal("a later dump with 0x18d must not turn the verdict green")
	}
}
