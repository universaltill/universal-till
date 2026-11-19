package bundled

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Finding 19: under the bundledplugins tag a missing bundle must be a COMPILE
// error, not a silent empty set. embed_on.go therefore embeds every locked
// file by NAME (go:embed fails the build when a named file is absent). This
// untagged test keeps that list in step with bundled.lock.json: a lock entry
// or pinned file with no matching go:embed path fails here.
func TestEmbedOnNamesEveryLockedFile(t *testing.T) {
	src, err := os.ReadFile("embed_on.go")
	if err != nil {
		t.Fatal(err)
	}
	var embedded []string
	for _, m := range regexp.MustCompile(`(?m)^//go:embed (.+)$`).FindAllStringSubmatch(string(src), -1) {
		embedded = append(embedded, strings.Fields(m[1])...)
	}
	set := map[string]bool{}
	for _, p := range embedded {
		if strings.Contains(p, "*") || strings.HasPrefix(p, "all:") {
			t.Fatalf("embed pattern %q is a glob/dir: a missing bundle would not be a compile error", p)
		}
		set[p] = true
	}
	l, err := compiledLock()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, e := range l.Plugins {
		want["embedded/"+e.ID+"/manifest.json"] = true
		want["embedded/"+e.ID+"/"+e.WasmPath] = true
		for rel := range e.Files {
			want["embedded/"+e.ID+"/"+rel] = true
		}
	}
	for p := range want {
		if !set[p] {
			t.Errorf("embed_on.go does not embed %s by name", p)
		}
	}
	for p := range set {
		if !want[p] {
			t.Errorf("embed_on.go embeds %s, which is not in the lock", p)
		}
	}
}
