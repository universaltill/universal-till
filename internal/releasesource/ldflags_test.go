package releasesource

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLdflagsSymbolPath pins the -X symbol an independent operator stamps
// (ut-docs#4080): renaming buildinfo.ReleasesBaseURL breaks this test.
func TestLdflagsSymbolPath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "printsource")
	flag := "-X github.com/universaltill/universal-till/internal/buildinfo.ReleasesBaseURL=https://api.github.com/repos/acme/till/releases"
	if out, err := exec.Command("go", "build", "-ldflags", flag, "-o", bin, "./testdata/printsource").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), "https://api.github.com/repos/acme/till/releases/latest"; got != want {
		t.Errorf("printsource = %q, want %q", got, want)
	}
}
