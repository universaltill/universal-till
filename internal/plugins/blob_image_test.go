package plugins

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
)

// OpenBlobImage (ut-docs#3957): core serves a plugin's own blob as a
// suggestion thumbnail — image types only (sniffed), size-capped, never
// another plugin's blob or a path outside the store.

func blobImageRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prev := paths.DataDir()
	paths.Init(root)
	t.Cleanup(func() { paths.Init(prev) })
	return root
}

func writeBlob(t *testing.T, root, pluginID, name string, content []byte) {
	t.Helper()
	dir := filepath.Join(root, "plugin-data", pluginID, "blobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

var (
	pngHead  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpegHead = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
)

func TestOpenBlobImage_ServesOwnImage_3957(t *testing.T) {
	root := blobImageRoot(t)
	content := append(append([]byte{}, pngHead...), bytes.Repeat([]byte{1}, 600)...)
	writeBlob(t, root, "com.test.a", "oat.png", content)

	f, ctype, size, err := OpenBlobImage("com.test.a", "oat.png", 1<<20)
	if err != nil {
		t.Fatalf("OpenBlobImage: %v", err)
	}
	defer func() { _ = f.Close() }()
	if ctype != "image/png" || size != int64(len(content)) {
		t.Fatalf("ctype=%q size=%d, want image/png %d", ctype, size, len(content))
	}
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("read back %d bytes (err %v): the file must be rewound to the start after sniffing", len(got), err)
	}
}

func TestOpenBlobImage_Refusals_3957(t *testing.T) {
	root := blobImageRoot(t)
	writeBlob(t, root, "com.test.a", "notes.txt", []byte("hello, plain text"))
	writeBlob(t, root, "com.test.a", "page.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	writeBlob(t, root, "com.test.a", "page.html", []byte(`<!DOCTYPE html><script>alert(1)</script>`))
	writeBlob(t, root, "com.test.a", "big.jpg", append(append([]byte{}, jpegHead...), bytes.Repeat([]byte{0}, 4096)...))
	writeBlob(t, root, "com.test.a", "empty.png", nil)
	writeBlob(t, root, "com.test.a", "~put-1", pngHead)
	writeBlob(t, root, "com.test.b", "secret.png", pngHead)
	if err := os.MkdirAll(filepath.Join(root, "plugin-data", "com.test.a", "blobs", "dir.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A file outside the store the traversal cases aim at.
	if err := os.WriteFile(filepath.Join(root, "plugin-data", "outside.png"), pngHead, 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, plugin, blob string
		want               error
	}{
		{"not an image", "com.test.a", "notes.txt", ErrBlobNotImage},
		{"svg is never served", "com.test.a", "page.svg", ErrBlobNotImage},
		{"html is never served", "com.test.a", "page.html", ErrBlobNotImage},
		{"over the cap", "com.test.a", "big.jpg", ErrBlobTooLarge},
		{"empty", "com.test.a", "empty.png", ErrBlobNotImage},
		{"missing", "com.test.a", "nope.png", ErrBlobNotFound},
		{"a directory", "com.test.a", "dir.png", ErrBlobNotFound},
		{"another plugin's blob by name", "com.test.a", "secret.png", ErrBlobNotFound},
		{"in-flight put", "com.test.a", "~put-1", ErrBlobNotFound},
		{"traversal in the name", "com.test.a", "../outside.png", ErrBlobNotFound},
		{"traversal via ..", "com.test.a", "..", ErrBlobNotFound},
		{"separator in the name", "com.test.a", "x/../../outside.png", ErrBlobNotFound},
		{"backslash in the name", "com.test.a", `..\outside.png`, ErrBlobNotFound},
		{"upper case name", "com.test.a", "OAT.PNG", ErrBlobNotFound},
		{"windows device name", "com.test.a", "con.png", ErrBlobNotFound},
		{"traversal in the plugin id", "../plugin-data", "outside.png", ErrBlobNotFound},
		{"plugin id with a separator", "com.test.a/../com.test.b", "secret.png", ErrBlobNotFound},
		{"empty plugin id", "", "outside.png", ErrBlobNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _, _, err := OpenBlobImage(c.plugin, c.blob, 2048)
			if f != nil {
				_ = f.Close()
				t.Fatal("got an open file, want a refusal")
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestOpenBlobImage_RefusesSymlink_3957(t *testing.T) {
	root := blobImageRoot(t)
	writeBlob(t, root, "com.test.b", "secret.png", pngHead)
	dir := filepath.Join(root, "plugin-data", "com.test.a", "blobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "plugin-data", "com.test.b", "blobs", "secret.png"), filepath.Join(dir, "link.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if f, _, _, err := OpenBlobImage("com.test.a", "link.png", 2048); !errors.Is(err, ErrBlobNotFound) {
		if f != nil {
			_ = f.Close()
		}
		t.Fatalf("err = %v, want ErrBlobNotFound for a symlink out of the store", err)
	}
}

func TestValidBlobName_Exported_3957(t *testing.T) {
	for name, want := range map[string]bool{"oat.png": true, "a": true, "": false, "..": false, "A.png": false, "x/y": false, "con": false} {
		if got := ValidBlobName(name); got != want {
			t.Errorf("ValidBlobName(%q) = %v, want %v", name, got, want)
		}
	}
}
