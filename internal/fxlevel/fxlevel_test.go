package fxlevel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gib = uint64(1) << 30

// TestDetect is the ADR-0119 §4 host rule: Light on a Pi, ≤ 2 cores or
// ≤ 2 GiB of known RAM; everything else Full. Host signals alone never give
// Balanced (only the page probe can), and unknown RAM is never a signal.
func TestDetect(t *testing.T) {
	cases := []struct {
		name       string
		s          Signals
		wantLevel  string
		wantReason string
	}{
		{"pi 5 with plenty of everything", Signals{Cores: 4, RAMBytes: 8 * gib, PiModel: "Raspberry Pi 5 Model B Rev 1.0"}, Light, "pi,cores=4,ram=8g"},
		{"fast desktop", Signals{Cores: 16, RAMBytes: 32 * gib}, Full, "cores=16,ram=32g"},
		{"four cores four gigs is not demoted", Signals{Cores: 4, RAMBytes: 4 * gib}, Full, "cores=4,ram=4g"},
		{"two cores", Signals{Cores: 2, RAMBytes: 8 * gib}, Light, "cores=2,ram=8g"},
		{"one core", Signals{Cores: 1, RAMBytes: 8 * gib}, Light, "cores=1,ram=8g"},
		{"three cores", Signals{Cores: 3, RAMBytes: 8 * gib}, Full, "cores=3,ram=8g"},
		{"exactly 2 GiB", Signals{Cores: 8, RAMBytes: 2 * gib}, Light, "cores=8,ram=2g"},
		{"a 2 GB box reports a little under 2 GiB", Signals{Cores: 8, RAMBytes: 1900 << 20}, Light, "cores=8,ram=2g"},
		{"just over 2 GiB", Signals{Cores: 8, RAMBytes: 2*gib + 1<<20}, Full, "cores=8,ram=2g"},
		{"half a gig rounds up to 1g", Signals{Cores: 8, RAMBytes: 512 << 20}, Light, "cores=8,ram=1g"},
		{"unknown RAM is never a light signal", Signals{Cores: 8}, Full, "cores=8"},
		{"unknown RAM with few cores still light on cores", Signals{Cores: 2}, Light, "cores=2"},
		{"unknown cores (0) is not a signal", Signals{RAMBytes: 8 * gib}, Full, "ram=8g"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Detect(c.s)
			if got.Level != c.wantLevel {
				t.Errorf("level = %q, want %q", got.Level, c.wantLevel)
			}
			if got.Level == Balanced {
				t.Errorf("host detection returned balanced — only the page probe may (ADR-0119 §4)")
			}
			if got.Reason != c.wantReason {
				t.Errorf("reason = %q, want %q", got.Reason, c.wantReason)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	a := Signals{Cores: 4, RAMBytes: 8 * gib, PiModel: "Raspberry Pi 5 Model B Rev 1.0", GOOS: "linux", GOARCH: "arm64"}
	fp := a.Fingerprint()
	for _, want := range []string{"cores=4", "ram=8g", "Raspberry Pi 5 Model B Rev 1.0", "linux/arm64"} {
		if !strings.Contains(fp, want) {
			t.Errorf("fingerprint %q lacks %q", fp, want)
		}
	}
	same := a
	same.RAMBytes = 8*gib - 100<<20 // same bucket
	if same.Fingerprint() != fp {
		t.Errorf("RAM wobble inside one bucket changed the fingerprint: %q vs %q", same.Fingerprint(), fp)
	}
	for name, mut := range map[string]func(*Signals){
		"cores":  func(s *Signals) { s.Cores = 8 },
		"ram":    func(s *Signals) { s.RAMBytes = 4 * gib },
		"model":  func(s *Signals) { s.PiModel = "Raspberry Pi 4 Model B Rev 1.4" },
		"goos":   func(s *Signals) { s.GOOS = "android" },
		"goarch": func(s *Signals) { s.GOARCH = "amd64" },
	} {
		b := a
		mut(&b)
		if b.Fingerprint() == fp {
			t.Errorf("changing %s did not change the fingerprint", name)
		}
	}
}

func TestParseMemTotal(t *testing.T) {
	cases := map[string]struct {
		in   string
		want uint64
		ok   bool
	}{
		"normal":  {"MemTotal:        8013504 kB\nMemFree:  100 kB\n", 8013504 * 1024, true},
		"missing": {"MemFree:  100 kB\n", 0, false},
		"garbage": {"MemTotal: lots kB\n", 0, false},
		"zero":    {"MemTotal: 0 kB\n", 0, false},
		"empty":   {"", 0, false},
	}
	for name, c := range cases {
		got, ok := parseMemTotal([]byte(c.in))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: parseMemTotal = (%d,%v), want (%d,%v)", name, got, ok, c.want, c.ok)
		}
	}
}

// withSeams points every host signal at a controlled value.
func withSeams(t *testing.T, cores int, meminfo string, pi string, goos string) {
	t.Helper()
	oldCPU, oldMem, oldPi, oldOS, oldArch := numCPU, meminfoPath, piModel, hostGOOS, hostGOARCH
	t.Cleanup(func() {
		numCPU, meminfoPath, piModel, hostGOOS, hostGOARCH = oldCPU, oldMem, oldPi, oldOS, oldArch
	})
	numCPU = func() int { return cores }
	p := filepath.Join(t.TempDir(), "does-not-exist")
	if meminfo != "" {
		p = filepath.Join(t.TempDir(), "meminfo")
		if err := os.WriteFile(p, []byte(meminfo), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	meminfoPath = p
	piModel = func() string { return pi }
	hostGOOS, hostGOARCH = goos, "arm64"
}

func TestReadSignals(t *testing.T) {
	withSeams(t, 4, "MemTotal: 2097152 kB\n", "Raspberry Pi 4 Model B", "linux")
	s := ReadSignals()
	if s.Cores != 4 || s.RAMBytes != 2*gib || s.PiModel != "Raspberry Pi 4 Model B" || s.GOOS != "linux" || s.GOARCH != "arm64" {
		t.Fatalf("ReadSignals = %+v", s)
	}
}

func TestReadSignals_AndroidReadsMeminfo(t *testing.T) {
	withSeams(t, 8, "MemTotal: 2097152 kB\n", "", "android")
	if s := ReadSignals(); s.RAMBytes != 2*gib {
		t.Fatalf("android RAM = %d, want read from /proc/meminfo", s.RAMBytes)
	}
}

// Windows/macOS RAM is a follow-up card: until then it is unknown, and a
// stray meminfo-shaped file must not be read there.
func TestReadSignals_OtherOSRAMUnknown(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		withSeams(t, 8, "MemTotal: 1024 kB\n", "", goos)
		if s := ReadSignals(); s.RAMBytes != 0 {
			t.Errorf("%s: RAM = %d, want 0 (unknown)", goos, s.RAMBytes)
		}
	}
}

func TestReadSignals_MissingMeminfoIsUnknown(t *testing.T) {
	withSeams(t, 8, "", "", "linux")
	s := ReadSignals()
	if s.RAMBytes != 0 {
		t.Fatalf("RAM = %d, want unknown", s.RAMBytes)
	}
	if Detect(s).Level != Full {
		t.Fatalf("unknown RAM on an 8-core box demoted it")
	}
}

func TestValid(t *testing.T) {
	for _, v := range []string{Auto, Full, Balanced, Light} {
		if !ValidSetting(v) {
			t.Errorf("ValidSetting(%q) = false", v)
		}
	}
	for _, v := range []string{"", "fx-full", "LIGHT", "auto ", "none"} {
		if ValidSetting(v) {
			t.Errorf("ValidSetting(%q) = true", v)
		}
	}
	if ValidResolved(Auto) {
		t.Error("auto is a setting, never a resolved level")
	}
	for _, v := range []string{Full, Balanced, Light} {
		if !ValidResolved(v) {
			t.Errorf("ValidResolved(%q) = false", v)
		}
	}
}

func TestReasonTokens(t *testing.T) {
	got := ParseReason("pi,cores=4,ram=8g")
	want := []ReasonToken{{Name: "pi"}, {Name: "cores", Value: 4}, {Name: "ram", Value: 8}}
	if len(got) != len(want) {
		t.Fatalf("ParseReason = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Unknown or malformed tokens are dropped, never rendered raw.
	if got := ParseReason("webkitgtk,cores=x,ram=8,bogus=3,"); len(got) != 0 {
		t.Errorf("malformed reason parsed to %+v, want nothing", got)
	}
}
