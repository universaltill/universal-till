package plugins

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#4563: `tar -C dir -czf x.tgz .` writes a bare "./" directory entry
// first (and "./manifest.json"-style names after it). filepath.Join(dest, "./")
// is dest itself, so the old prefix check refused every such bundle with
// "illegal file path in archive: ./" — a scan-passed, signed release no till
// could install. A root directory entry is now a no-op; anything that
// escapes the root, or a non-directory entry naming the root, is still refused.

type tarEntry struct {
	name string
	flag byte
	data []byte
}

func tarGzBytes(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.flag, Mode: 0o644, Size: int64(len(e.data))}
		if e.flag == tar.TypeDir {
			hdr.Mode = 0o755
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", e.name, err)
		}
		if len(e.data) > 0 && e.flag != tar.TypeDir {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatalf("tar write %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// dotRootTar mirrors `tar -C dir -czf x.tgz .` for a two-file bundle.
func dotRootTar(t *testing.T, manifest []byte) []byte {
	return tarGzBytes(t, []tarEntry{
		{name: "./", flag: tar.TypeDir},
		{name: "./manifest.json", flag: tar.TypeReg, data: manifest},
		{name: "./assets/", flag: tar.TypeDir},
		{name: "./assets/icon.svg", flag: tar.TypeReg, data: []byte("<svg/>")},
	})
}

func writeBytes(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestExtractMarketplaceTarGzAcceptsDotRootEntry(t *testing.T) {
	archive := writeBytes(t, filepath.Join(t.TempDir(), "bundle.tgz"), dotRootTar(t, []byte(`{}`)))
	dest := t.TempDir()
	if err := extractMarketplaceTarGz(archive, dest); err != nil {
		t.Fatalf("extract bundle with ./ entry: %v", err)
	}
	for _, rel := range []string{"manifest.json", "assets/icon.svg"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Fatalf("%s not extracted: %v", rel, err)
		}
	}
}

func TestExtractMarketplaceTarGzStillRefusesEscapes(t *testing.T) {
	cases := map[string][]tarEntry{
		"parent":          {{name: "../x", flag: tar.TypeReg, data: []byte("x")}},
		"dot then parent": {{name: "./", flag: tar.TypeDir}, {name: "./../x", flag: tar.TypeReg, data: []byte("x")}},
		"parent dir":      {{name: "../", flag: tar.TypeDir}},
		"root as a file":  {{name: ".", flag: tar.TypeReg, data: []byte("x")}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			archive := writeBytes(t, filepath.Join(t.TempDir(), "evil.tgz"), tarGzBytes(t, entries))
			err := extractMarketplaceTarGz(archive, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "illegal file path") {
				t.Fatalf("want illegal file path, got %v", err)
			}
		})
	}
}

func TestImportAcceptsTarWithDotRootEntry(t *testing.T) {
	imp, _ := newTestImporter(t)
	archive := writeBytes(t, filepath.Join(t.TempDir(), "plugin.tar.gz"),
		dotRootTar(t, validImportManifest("com.test.dotroot")))
	res, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if err != nil {
		t.Fatalf("import tar with ./ entry: %v", err)
	}
	if res.PluginID != "com.test.dotroot" {
		t.Fatalf("plugin id = %q", res.PluginID)
	}
}

func TestImportRefusesTarDotThenParent(t *testing.T) {
	imp, _ := newTestImporter(t)
	archive := writeBytes(t, filepath.Join(t.TempDir(), "evil.tar.gz"), tarGzBytes(t, []tarEntry{
		{name: "./", flag: tar.TypeDir},
		{name: "./../evil.txt", flag: tar.TypeReg, data: []byte("escape")},
	}))
	_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if err == nil || !strings.Contains(err.Error(), "illegal file path") {
		t.Fatalf("./../ traversal not rejected: %v", err)
	}
}

func zipBytes(t *testing.T, names []string, data map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data[name]); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestImportAcceptsZipWithDotRootEntry(t *testing.T) {
	imp, _ := newTestImporter(t)
	archive := writeBytes(t, filepath.Join(t.TempDir(), "plugin.zip"), zipBytes(t,
		[]string{"./", "./manifest.json"},
		map[string][]byte{"./manifest.json": validImportManifest("com.test.dotrootzip")}))
	res, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if err != nil {
		t.Fatalf("import zip with ./ entry: %v", err)
	}
	if res.PluginID != "com.test.dotrootzip" {
		t.Fatalf("plugin id = %q", res.PluginID)
	}
}

func TestImportRefusesZipRootEscapes(t *testing.T) {
	cases := map[string][]string{
		"dot then parent": {"./", "./../evil.txt"},
		"root as a file":  {"."},
	}
	for name, names := range cases {
		t.Run(name, func(t *testing.T) {
			imp, _ := newTestImporter(t)
			data := map[string][]byte{}
			for _, n := range names {
				if !strings.HasSuffix(n, "/") {
					data[n] = []byte("escape")
				}
			}
			archive := writeBytes(t, filepath.Join(t.TempDir(), "evil.zip"), zipBytes(t, names, data))
			_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
			if err == nil || !strings.Contains(err.Error(), "illegal file path") {
				t.Fatalf("want illegal file path, got %v", err)
			}
		})
	}
}
