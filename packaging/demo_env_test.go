package packaging

// ADR-0113 §1.2 (ut-docs#2687): demo mode must not be switchable on by a
// production install. The start gate (internal/app checkDemoGate) already
// needs a marker file and a flagged database a real till never has; this is
// the belt to that: no production packaging — .deb/systemd/kiosk scripts,
// desktop shell, macOS/Windows launchers, Android launch config, Docker,
// release config, the shipped pos.env templates — mentions UT_DEMO at all.
// Strict, comments included: a commented "# UT_DEMO=1" in a pos.env
// template is one uncomment away from being set.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var demoEnvRe = regexp.MustCompile(`UT_DEMO`)

// productionPackagingRoots are repo-root-relative files and directories
// that ship to, or launch, a real till. Directories are walked recursively;
// _test.go files are skipped (they never ship).
var productionPackagingRoots = []string{
	"packaging",               // .deb scripts, systemd unit, kiosk, desktop entry, macOS, Windows, pos.env.example
	"Dockerfile",              // container image
	"docker-compose.yml",      // compose deployment
	"docker-compose.edge.yml", // edge compose deployment
	".goreleaser.yaml",        // release/nfpm packaging config
	"pos.env",                 // shipped env template
	"pos.env.example",         // shipped env template
	"android/app/src/main",    // Android manifest + launch code
	"mobile",                  // gomobile entry the Android app starts the till through
	"cmd/unitill-desktop",     // desktop shell that spawns the till
}

// demoEnvMentions returns "<file>:<line>" for every line under root's
// productionPackagingRoots mentioning UT_DEMO. Binary files (a NUL byte in
// the content) are skipped.
func demoEnvMentions(t *testing.T, root string, roots []string) (hits []string, scanned int) {
	t.Helper()
	for _, rel := range roots {
		start := filepath.Join(root, rel)
		if _, err := os.Stat(start); err != nil {
			t.Fatalf("production packaging path %s is gone (%v) — update productionPackagingRoots rather than let the scan go blind", rel, err)
		}
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.IndexByte(string(raw), 0) >= 0 {
				return nil // binary (icons, …)
			}
			scanned++
			for i, line := range strings.Split(string(raw), "\n") {
				if demoEnvRe.MatchString(line) {
					r, _ := filepath.Rel(root, p)
					hits = append(hits, r+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", rel, err)
		}
	}
	return hits, scanned
}

func TestNoProductionPackagingSetsUTDemo(t *testing.T) {
	hits, scanned := demoEnvMentions(t, "..", productionPackagingRoots)
	if scanned < 20 {
		t.Fatalf("scanned only %d packaging files — the scan has gone blind", scanned)
	}
	if len(hits) > 0 {
		t.Fatalf("production packaging mentions UT_DEMO (ADR-0113 §1.2 — demo mode is for the demo broker only):\n%s",
			strings.Join(hits, "\n"))
	}
}

// The scan itself catches a planted setting — a pos.env line, a systemd
// Environment= line, and a Go os.Setenv — so the test above can't pass by
// looking at nothing.
func TestDemoEnvScanCatchesAPlantedSetting(t *testing.T) {
	root := t.TempDir()
	plant := map[string]string{
		"packaging/pos.env.example":             "UT_STORE_NAME=Shop\n# UT_DEMO=1\n",
		"packaging/systemd/unitill-pos.service": "[Service]\nEnvironment=UT_DEMO=1\n",
		"mobile/mobile.go":                      "package mobile\nfunc f() { _ = os.Setenv(\"UT_DEMO\", \"1\") }\n",
		"mobile/mobile_test.go":                 "package mobile\n// UT_DEMO in a test never ships\n",
	}
	for rel, content := range plant {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hits, _ := demoEnvMentions(t, root, []string{"packaging", "mobile"})
	if len(hits) != 3 {
		t.Fatalf("want 3 hits (pos.env comment, systemd Environment=, os.Setenv; not the _test.go), got %d: %v", len(hits), hits)
	}
}
