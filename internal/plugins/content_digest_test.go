package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree plants files (slash paths relative to the returned dir).
func writeTree(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// contentDigestGoldenVector is the ADR-0165 §2 cross-repo vector: ut-cloud's
// tar-entry digest must produce the same string for the same files
// (ut-docs#4546). Computed with coreutils, not with this code:
//
//	printf 'hello\n' > a.txt; mkdir dir; printf 'world' > dir/b.bin
//	sha256sum a.txt dir/b.bin
//	  5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03  a.txt
//	  486ea46224d1bb4fb680f34f7c9ad96a8f24ec88be73ea8e5a6c65260e9cb8a7  dir/b.bin
//	(that listing, two lines, each "\n"-terminated) | sha256sum
//	  66b6b50e2624a98a817af14426a4427e8cd77ef06ea02bd7d20d80dc773b38cf
const contentDigestGoldenVector = "sha256:66b6b50e2624a98a817af14426a4427e8cd77ef06ea02bd7d20d80dc773b38cf"

func TestComputeContentDigest_GoldenVector(t *testing.T) {
	dir := writeTree(t, map[string][]byte{
		"a.txt":         []byte("hello\n"),
		"dir/b.bin":     []byte("world"),
		"manifest.json": []byte(`{"id":"excluded"}`),
	})
	got, err := ComputeContentDigest(dir)
	if err != nil {
		t.Fatalf("ComputeContentDigest: %v", err)
	}
	if got != contentDigestGoldenVector {
		t.Fatalf("digest = %s, want %s", got, contentDigestGoldenVector)
	}
}

// The root manifest.json is excluded whatever it holds; a manifest.json
// anywhere else is an ordinary covered file.
func TestComputeContentDigest_OnlyRootManifestExcluded(t *testing.T) {
	base := map[string][]byte{"a.txt": []byte("hello\n"), "dir/b.bin": []byte("world")}
	withManifest := map[string][]byte{"a.txt": []byte("hello\n"), "dir/b.bin": []byte("world"), "manifest.json": []byte("anything")}
	a, err := ComputeContentDigest(writeTree(t, base))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComputeContentDigest(writeTree(t, withManifest))
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a != contentDigestGoldenVector {
		t.Fatalf("root manifest.json changed the digest: %s vs %s", a, b)
	}
	nested := map[string][]byte{"a.txt": []byte("hello\n"), "dir/b.bin": []byte("world"), "sub/manifest.json": []byte("x")}
	c, err := ComputeContentDigest(writeTree(t, nested))
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("a nested manifest.json must be covered")
	}
}

// An empty package (manifest only) digests the empty listing; an empty
// directory changes nothing (directories are not covered).
func TestComputeContentDigest_EmptyListingAndEmptyDirs(t *testing.T) {
	dir := writeTree(t, map[string][]byte{"manifest.json": []byte("{}")})
	got, err := ComputeContentDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	const empty = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != empty {
		t.Fatalf("empty package digest = %s, want %s", got, empty)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := ComputeContentDigest(dir); got != empty {
		t.Fatalf("an empty directory changed the digest: %s", got)
	}
}

// Every byte of every covered file, and its path, counts.
func TestComputeContentDigest_ChangesWithContentOrPath(t *testing.T) {
	cases := map[string]map[string][]byte{
		"byte changed":  {"a.txt": []byte("hellO\n"), "dir/b.bin": []byte("world")},
		"file renamed":  {"a.txt": []byte("hello\n"), "dir/c.bin": []byte("world")},
		"file added":    {"a.txt": []byte("hello\n"), "dir/b.bin": []byte("world"), "x": nil},
		"file removed":  {"a.txt": []byte("hello\n")},
		"moved to root": {"a.txt": []byte("hello\n"), "b.bin": []byte("world")},
	}
	for name, files := range cases {
		got, err := ComputeContentDigest(writeTree(t, files))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got == contentDigestGoldenVector {
			t.Fatalf("%s: digest unchanged", name)
		}
	}
}

func TestComputeContentDigest_RefusesSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name, link, target string
	}{
		{"file symlink", "link.txt", "a.txt"},
		{"dir symlink", "linkdir", "dir"},
		{"root manifest symlink", "manifest.json", "a.txt"},
		{"dangling symlink", "dangling", "nowhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, map[string][]byte{"a.txt": []byte("hello\n"), "dir/b.bin": []byte("world")})
			if err := os.Symlink(tc.target, filepath.Join(dir, tc.link)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if got, err := ComputeContentDigest(dir); err == nil {
				t.Fatalf("symlink accepted, digest %s", got)
			} else if !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("error should name the symlink: %v", err)
			}
		})
	}
}

// A name with a backslash or a newline is refused (ADR-0165 §2): either
// could make the listing ambiguous between the till and the marketplace.
func TestComputeContentDigest_RefusesBadNames(t *testing.T) {
	for _, name := range []string{`a\b.txt`, "a\nb.txt"} {
		dir := writeTree(t, map[string][]byte{"a.txt": []byte("hello\n")})
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Skipf("filesystem refuses %q: %v", name, err)
		}
		if got, err := ComputeContentDigest(dir); err == nil {
			t.Fatalf("name %q accepted, digest %s", name, got)
		}
	}
}

func TestComputeContentDigest_MissingDir(t *testing.T) {
	if _, err := ComputeContentDigest(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing directory accepted")
	}
}

func TestValidateContentPath(t *testing.T) {
	for _, ok := range []string{"a.txt", "dir/b.bin", "locales/de.json", ".hidden"} {
		if err := validateContentPath(ok); err != nil {
			t.Fatalf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "/abs", "../up", "a/../b", "a//b", "./a", "a/", `a\b`, "a\nb", "a/.."} {
		if err := validateContentPath(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}
