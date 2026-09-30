package procrestart

import (
	"go/build"
	"os"
	"strings"
	"testing"
)

// ut-docs#3220 guard: no platform may show a restart button that can't
// work. The file that sets `supported = true` (the re-exec path) must build
// for desktop/server targets and never for iOS, Android or Windows — there
// the till has no exec, and only a registered restarter (mobile package)
// may make Supported() true. Every target must still get exactly one
// `supported` declaration, or the package wouldn't build there.
func TestReexecSupportedOnlyWhereExecWorks(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var trueFiles, allFiles []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(string(src), "const supported = true"):
			trueFiles = append(trueFiles, name)
			allFiles = append(allFiles, name)
		case strings.Contains(string(src), "const supported = false"):
			allFiles = append(allFiles, name)
		}
	}
	if len(trueFiles) == 0 {
		t.Fatal("no file declares `const supported = true` — the guard is looking at the wrong thing")
	}

	matches := func(goos, file string) bool {
		ctx := build.Default
		ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = goos, "arm64", true
		ok, err := ctx.MatchFile(".", file)
		if err != nil {
			t.Fatalf("MatchFile(%s, %s): %v", goos, file, err)
		}
		return ok
	}
	for _, goos := range []string{"ios", "android", "windows"} {
		for _, f := range trueFiles {
			if matches(goos, f) {
				t.Errorf("%s builds for %s: that target has no in-place exec, so Supported() would show a restart button that does nothing", f, goos)
			}
		}
	}
	for _, goos := range []string{"linux", "darwin", "freebsd", "ios", "android", "windows"} {
		n := 0
		for _, f := range allFiles {
			if matches(goos, f) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d files declare `supported`, want exactly 1", goos, n)
		}
	}
	for _, goos := range []string{"linux", "darwin"} {
		ok := false
		for _, f := range trueFiles {
			ok = ok || matches(goos, f)
		}
		if !ok {
			t.Errorf("%s lost its re-exec restart", goos)
		}
	}
}
