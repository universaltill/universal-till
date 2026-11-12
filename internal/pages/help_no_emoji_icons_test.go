package pages

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ut-docs#1431: the rail buttons are SVG icons, never emoji, so a help topic
// that says "press the bug emoji" tells the reader to look for something that
// does not exist on screen. Name the button by its label instead.
func TestHelpDoesNotNameRailButtonsByEmoji(t *testing.T) {
	// Anchored on this file, not the cwd: other tests in the package chdir.
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(self), "..", "..", "web", "help")
	banned := []string{"\U0001F41E", "\U0001F6CE", "\U0001F464"} // bug, bellhop bell, bust in silhouette
	scanned := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		scanned++
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, e := range banned {
				if strings.Contains(line, e) {
					t.Errorf("%s:%d contains %q: name the button by its label (and an icon description), not an emoji", p, i+1, e)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatalf("no help topics found under %s: the scan would pass vacuously", root)
	}
}
