package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rootEntry is one raw tar header for the ut-docs#4565 regression tests.
type rootEntry struct {
	name string
	dir  bool
	body string
}

func writeRootTarGz(t *testing.T, entries []rootEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.dir {
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if !e.dir {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// `tar czf x -C stage .` (nightly.yml) writes "./" first (ut-docs#4565).
func TestExtractTarGz_AcceptsRootDotSlashEntry_4565(t *testing.T) {
	src := writeRootTarGz(t, []rootEntry{
		{name: "./", dir: true},
		{name: "./unitill", body: "BIN"},
		{name: "./web/", dir: true},
		{name: "./web/x.txt", body: "WEB"},
	})
	dst := t.TempDir()
	if err := extractTarGz(src, dst); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	for f, want := range map[string]string{"unitill": "BIN", "web/x.txt": "WEB"} {
		got, err := os.ReadFile(filepath.Join(dst, f))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", f, got, err, want)
		}
	}
}

func TestExtractTarGz_StillRefusesUnsafeEntries_4565(t *testing.T) {
	cases := map[string][]rootEntry{
		"dotslash then dotdot file": {{name: "./", dir: true}, {name: "./../x", body: "x"}},
		"dotdot file":               {{name: "../x", body: "x"}},
		"dotdot dir":                {{name: "../", dir: true}},
		"dot as regular file":       {{name: ".", body: "x"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "dst")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			err := extractTarGz(writeRootTarGz(t, entries), dst)
			if err == nil || !strings.Contains(err.Error(), "unsafe path") {
				t.Fatalf("err = %v, want unsafe path", err)
			}
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(dst), "x")); statErr == nil {
				t.Error("file escaped dst")
			}
		})
	}
}

// Other spellings of the root as a directory entry are skipped the same way.
func TestExtractTarGz_AcceptsOtherRootDirSpellings_4565(t *testing.T) {
	for _, name := range []string{".", "a/.."} {
		t.Run(name, func(t *testing.T) {
			src := writeRootTarGz(t, []rootEntry{{name: name, dir: true}, {name: "unitill", body: "BIN"}})
			dst := t.TempDir()
			if err := extractTarGz(src, dst); err != nil {
				t.Fatalf("extractTarGz: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(dst, "unitill")); err != nil || string(got) != "BIN" {
				t.Errorf("unitill = %q, %v", got, err)
			}
		})
	}
}
