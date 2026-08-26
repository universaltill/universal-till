package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadUptimeParsesProcFormat: /proc/uptime is "<up> <idle>"; we want the
// first field, and a missing second field must not break it.
func TestReadUptimeParsesProcFormat(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    time.Duration
		wantErr bool
	}{
		{"two fields", "1234.56 9876.54\n", 1234560 * time.Millisecond, false},
		{"single field", "42.00\n", 42 * time.Second, false},
		{"no trailing newline", "7.5 1.0", 7500 * time.Millisecond, false},
		{"garbage", "not-a-number 1.0\n", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "uptime")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := readUptimeFrom(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("readUptimeFrom(%q) = %v, want error", tc.content, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("readUptimeFrom(%q): %v", tc.content, err)
			}
			if got != tc.want {
				t.Errorf("readUptimeFrom(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// TestReadUptimeMissingFileErrors: an unreadable /proc/uptime must surface an
// error so the caller can start immediately rather than hang forever — a till
// that opens late is bad, one that never opens is worse.
func TestReadUptimeMissingFileErrors(t *testing.T) {
	if _, err := readUptimeFrom(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("readUptimeFrom(missing) = nil error, want error")
	}
}

// TestGateDurationHonoursEnv covers the operator override, including the
// documented "0 disables" case and rejection of nonsense (which must fall
// back to the default, never to zero — a typo must not silently disable the
// gate that stops a shop seeing an unescapable white screen, ut-docs#1093),
// AND the too-large case (independent review, same ticket): a
// seconds/milliseconds mixup or an oversized value must fall back to the
// default too, not hold for hours or (via Duration overflow) silently
// disable the gate the same way a negative value would.
func TestGateDurationHonoursEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{"unset uses default", false, "", defaultMinUptime},
		{"explicit seconds", true, "15", 15 * time.Second},
		{"zero disables", true, "0", 0},
		{"whitespace tolerated", true, "  30  ", 30 * time.Second},
		{"negative falls back to default", true, "-5", defaultMinUptime},
		{"garbage falls back to default", true, "soon", defaultMinUptime},
		{"exactly at the cap is honoured", true, "600", 600 * time.Second},
		{"one past the cap falls back to default", true, "601", defaultMinUptime},
		{"units-confusion typo falls back to default", true, "60000", defaultMinUptime},
		{"large enough to overflow Duration falls back to default", true, "9223372037", defaultMinUptime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(minUptimeEnv, tc.val)
			} else {
				os.Unsetenv(minUptimeEnv)
			}
			if got := gateDuration(); got != tc.want {
				t.Errorf("gateDuration() with %s=%q = %v, want %v", minUptimeEnv, tc.val, got, tc.want)
			}
		})
	}
}

// TestHoldFor covers waitForSafeStartup's compare-and-sleep decision in
// isolation (independent review, ut-docs#1093): it was previously untested
// because it was entangled with the real clock and /proc/uptime.
func TestHoldFor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		up, min  time.Duration
		wantWait time.Duration
	}{
		{"already past the threshold", 90 * time.Second, 60 * time.Second, 0},
		{"exactly at the threshold", 60 * time.Second, 60 * time.Second, 0},
		{"still short of the threshold", 20 * time.Second, 60 * time.Second, 40 * time.Second},
		{"zero uptime, zero min", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := holdFor(tc.up, tc.min); got != tc.wantWait {
				t.Errorf("holdFor(%v, %v) = %v, want %v", tc.up, tc.min, got, tc.wantWait)
			}
		})
	}
}
