package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// ut-docs#5162 review: the server renders a restored copy's catalogue locked
// (mainTillGone), so the chip-following script in base.html must keep it
// locked when the chip reads "rejoin", not only "unreachable" — otherwise the
// first chip load unlocks fieldsets whose every save is then refused.
func TestBaseLayout_RejoinStateKeepsMainTillLock(t *testing.T) {
	raw, err := os.ReadFile(baseLayoutPath(t))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	helper := regexp.MustCompile(`function mainTillLocks\(state\) \{\s*return ([^;]+);`).FindStringSubmatch(src)
	if helper == nil {
		t.Fatal("base.html has no mainTillLocks(state) helper")
	}
	for _, st := range []string{"'unreachable'", "'rejoin'"} {
		if !strings.Contains(helper[1], "state === "+st) {
			t.Errorf("mainTillLocks must lock for %s, got: %s", st, helper[1])
		}
	}
	// Every call that locks on a chip state goes through the helper ("away"
	// is the function's own parameter list).
	for _, m := range regexp.MustCompile(`applyMainTillLock\([^,]+,\s*([^)]+\)?)\)`).FindAllStringSubmatch(src, -1) {
		arg := strings.TrimSpace(m[1])
		if arg == "false" || arg == "true" || arg == "away" || strings.HasPrefix(arg, "mainTillLocks(") || strings.HasPrefix(arg, "locked") {
			continue
		}
		t.Errorf("applyMainTillLock called with %q; use mainTillLocks(state) so rejoin stays locked", arg)
	}
}

func baseLayoutPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "web", "ui", "layouts", "base.html")
}
