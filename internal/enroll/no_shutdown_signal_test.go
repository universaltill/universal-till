package enroll

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ADR-0200 section 2: "No shutdown or turn-off signal exists or is built."
// The cloud learns a till is gone only from last_seen going stale. This pins
// that no change-event or check-in code names such a signal.
func TestNoShutdownOrTurnOffSignalIsBuilt(t *testing.T) {
	banned := regexp.MustCompile(`(?i)(shutdown|turn.?off|going.?offline|goodbye|clean.?exit|last.?beat)`)
	// Words that are legitimate there: process/server shutdown plumbing.
	benign := regexp.MustCompile(`(?i)(wait out shutdown|\.Shutdown\()`)
	files := []string{"auto.go"}
	for _, rel := range []string{"../cloudsync/change_events.go", "../data/change_events_repo.go"} {
		files = append(files, rel)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.FromSlash(f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if banned.MatchString(line) && !benign.MatchString(line) {
				t.Errorf("%s:%d names a shutdown/turn-off signal (ADR-0200: none is built): %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
