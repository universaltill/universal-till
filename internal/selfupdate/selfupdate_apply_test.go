package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// These tests mutate package-level seams (osExecutable, reexecFn, releasesLatest,
// buildinfo.Version) — none of them may use t.Parallel. Everything is hermetic:
// all HTTP goes to local httptest servers, the "running binary" is a fake file
// in a t.TempDir, and re-exec is stubbed (a real one would replace the test
// process image).

// tarEntry is one file or directory for makeTarGz.
type tarEntry struct {
	name string
	body string
	dir  bool
	mode int64
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		hdr := &tar.Header{Name: e.name, Mode: mode}
		if e.dir {
			hdr.Typeflag = tar.TypeDir
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.body))
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
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// releaseServer serves a fake GitHub "latest release" plus its assets.
type releaseServer struct {
	srv    *httptest.Server
	tag    string
	assets map[string][]byte // asset name -> content, served at /asset/<name>; a nil body is listed in /latest but 404s on download
}

func newReleaseServer(t *testing.T, tag string, assets map[string][]byte) *releaseServer {
	t.Helper()
	rs := &releaseServer{tag: tag, assets: assets}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf(`{"tag_name":%q,"assets":[`, rs.tag))
		first := true
		for name := range rs.assets {
			if !first {
				sb.WriteString(",")
			}
			first = false
			sb.WriteString(fmt.Sprintf(`{"name":%q,"browser_download_url":%q}`, name, rs.srv.URL+"/asset/"+name))
		}
		sb.WriteString("]}")
		w.Write([]byte(sb.String()))
	})
	mux.HandleFunc("/asset/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/asset/")
		body, ok := rs.assets[name]
		if !ok || body == nil {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	rs.srv = httptest.NewServer(mux)
	t.Cleanup(rs.srv.Close)
	old := releasesLatest
	releasesLatest = rs.srv.URL + "/latest"
	t.Cleanup(func() { releasesLatest = old })
	return rs
}

// installFixture is a fake portable install: a directory holding the "current"
// binary and web/ assets, with the osExecutable seam pointing at it.
type installFixture struct {
	dir     string
	exe     string
	oldBin  string
	reexecd chan string
}

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "unitill-pos")
	f := &installFixture{dir: dir, exe: exe, oldBin: "OLD-BINARY-v1", reexecd: make(chan string, 1)}
	if err := os.WriteFile(exe, []byte(f.oldBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "index.html"), []byte("OLD-WEB"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldExec, oldReexec, oldDelay, oldVer := osExecutable, reexecFn, reexecDelay, buildinfo.Version
	osExecutable = func() (string, error) { return exe, nil }
	reexecFn = func(path string) error { f.reexecd <- path; return nil }
	reexecDelay = 5 * time.Millisecond
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() {
		osExecutable, reexecFn, reexecDelay, buildinfo.Version = oldExec, oldReexec, oldDelay, oldVer
	})
	return f
}

func archiveNameFor(version string) string {
	return fmt.Sprintf("unitill-pos_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

// The happy path: Apply downloads the release archive, verifies its checksum,
// swaps binary + web/ keeping .bak backups, and schedules a re-exec.
func TestApplySuccessSwapsBinaryAndWeb(t *testing.T) {
	fix := newInstallFixture(t)
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{
		{name: "unitill-pos", body: "NEW-BINARY-v2"},
		{name: "web", dir: true},
		{name: "web/index.html", body: "NEW-WEB"},
	})
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(sha256hex(archive) + "  " + name + "\n"),
	})

	if err := Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != "NEW-BINARY-v2" {
		t.Errorf("binary not swapped, content = %q", got)
	}
	if info, err := os.Stat(fix.exe); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("new binary not executable: %v %v", info, err)
	}
	if got, _ := os.ReadFile(fix.exe + ".bak"); string(got) != fix.oldBin {
		t.Errorf("backup missing/wrong, content = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(fix.dir, "web", "index.html")); string(got) != "NEW-WEB" {
		t.Errorf("web not swapped, content = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(fix.dir, "web.bak", "index.html")); string(got) != "OLD-WEB" {
		t.Errorf("web backup missing/wrong, content = %q", got)
	}
	select {
	case exe := <-fix.reexecd:
		if exe != fix.exe {
			t.Errorf("re-exec'd %q, want %q", exe, fix.exe)
		}
	case <-time.After(2 * time.Second):
		t.Error("re-exec never scheduled")
	}
}

func TestApplyUnsupportedInstall(t *testing.T) {
	old := osExecutable
	osExecutable = func() (string, error) { return "/usr/bin/unitill-pos", nil }
	t.Cleanup(func() { osExecutable = old })
	if err := Apply(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Apply on a .deb install = %v, want ErrUnsupported", err)
	}
}

func TestSupportedFalseWhenExecutableUnknown(t *testing.T) {
	old := osExecutable
	osExecutable = func() (string, error) { return "", errors.New("boom") }
	t.Cleanup(func() { osExecutable = old })
	if Supported() {
		t.Fatal("Supported() = true with an unlocatable executable")
	}
}

func TestApplyAlreadyLatest(t *testing.T) {
	newInstallFixture(t)
	newReleaseServer(t, "v0.1.0", map[string][]byte{}) // same as buildinfo.Version pinned by fixture
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already on the latest") {
		t.Fatalf("err = %v, want already-on-latest", err)
	}
}

func TestApplyNoArchiveForPlatform(t *testing.T) {
	fix := newInstallFixture(t)
	newReleaseServer(t, "v0.2.0", map[string][]byte{"something-else.zip": []byte("x")})
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no release archive") {
		t.Fatalf("err = %v, want no-release-archive", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
		t.Errorf("binary touched on failed update: %q", got)
	}
}

// SECURITY: a release with no checksums.txt must abort, not silently install an
// unverified archive. Every real release ships checksums.txt (.goreleaser.yaml
// name_template), so a missing one only ever means a broken or tampered release.
func TestApplyRefusesReleaseWithoutChecksums(t *testing.T) {
	fix := newInstallFixture(t)
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{{name: "unitill-pos", body: "NEW-BINARY-v2"}})
	newReleaseServer(t, "v0.2.0", map[string][]byte{name: archive}) // no checksums.txt
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksums.txt") {
		t.Fatalf("err = %v, want refusal over missing checksums.txt", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
		t.Errorf("unverified archive was installed: %q", got)
	}
}

func TestApplyDownloadFailureAborts(t *testing.T) {
	fix := newInstallFixture(t)
	name := archiveNameFor("0.2.0")
	// Archive listed in the release but 404s on download (nil body).
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		name:            nil,
		"checksums.txt": []byte("irrelevant"),
	})
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "download") {
		t.Fatalf("err = %v, want download failure", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
		t.Errorf("binary touched on failed download: %q", got)
	}
}

func TestApplyChecksumMismatchAborts(t *testing.T) {
	fix := newInstallFixture(t)
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{{name: "unitill-pos", body: "NEW-BINARY-v2"}})
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(strings.Repeat("0", 64) + "  " + name + "\n"),
	})
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
		t.Errorf("binary touched after checksum mismatch: %q", got)
	}
	if _, err := os.Stat(fix.exe + ".bak"); !os.IsNotExist(err) {
		t.Errorf(".bak created before verification passed: %v", err)
	}
}

func TestApplyArchiveMissingBinary(t *testing.T) {
	fix := newInstallFixture(t)
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{{name: "web", dir: true}, {name: "web/index.html", body: "W"}})
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(sha256hex(archive) + "  " + name + "\n"),
	})
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "update archive missing") {
		t.Fatalf("err = %v, want archive-missing-binary", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
		t.Errorf("binary touched: %q", got)
	}
}

func TestApplyBackupFailureLeavesBinary(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; read-only dir does not block rename")
	}
	fix := newInstallFixture(t)
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{{name: "unitill-pos", body: "NEW-BINARY-v2"}})
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(sha256hex(archive) + "  " + name + "\n"),
	})
	if err := os.Chmod(fix.dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fix.dir, 0o755) })
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "back up current binary") {
		t.Fatalf("err = %v, want backup failure", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
		t.Errorf("binary changed despite backup failure: %q", got)
	}
}

// fetchLatest error branches ------------------------------------------------

func TestFetchLatestHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	old := releasesLatest
	releasesLatest = srv.URL
	t.Cleanup(func() { releasesLatest = old })
	_, err := fetchLatest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v, want HTTP 502", err)
	}
}

func TestFetchLatestBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{nope"))
	}))
	t.Cleanup(srv.Close)
	old := releasesLatest
	releasesLatest = srv.URL
	t.Cleanup(func() { releasesLatest = old })
	if _, err := fetchLatest(context.Background()); err == nil {
		t.Fatal("want decode error, got nil")
	}
}

// Helper units ---------------------------------------------------------------

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.Write([]byte("payload"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	dst := filepath.Join(t.TempDir(), "f")
	if err := download(context.Background(), srv.URL+"/ok", dst); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "payload" {
		t.Errorf("downloaded %q", got)
	}
	if err := download(context.Background(), srv.URL+"/missing", dst); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("err = %v, want HTTP 404", err)
	}
}

func TestChecksumFor(t *testing.T) {
	body := "ABCDEF0123  unitill-pos_0.2.0_linux_amd64.tar.gz\nmalformed line\ndeadbeef  other.tar.gz\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	got, err := checksumFor(context.Background(), srv.URL, "unitill-pos_0.2.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("checksumFor: %v", err)
	}
	if got != "abcdef0123" { // lowercased
		t.Errorf("checksum = %q", got)
	}
	if _, err := checksumFor(context.Background(), srv.URL, "absent.tar.gz"); err == nil || !strings.Contains(err.Error(), "checksum not found") {
		t.Errorf("err = %v, want not-found", err)
	}
}

func TestVerifySHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := sha256hex([]byte("hello"))
	if err := verifySHA256(p, want); err != nil {
		t.Errorf("matching checksum rejected: %v", err)
	}
	if err := verifySHA256(p, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("err = %v, want mismatch", err)
	}
	if err := verifySHA256(filepath.Join(t.TempDir(), "absent"), want); err == nil {
		t.Error("missing file accepted")
	}
}

func TestExtractTarGz(t *testing.T) {
	dst := t.TempDir()
	src := filepath.Join(t.TempDir(), "a.tar.gz")
	archive := makeTarGz(t, []tarEntry{
		{name: "bin", body: "B"},
		{name: "web", dir: true},
		{name: "web/x.css", body: "C", mode: 0o644},
	})
	if err := os.WriteFile(src, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(src, dst); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "web", "x.css")); string(got) != "C" {
		t.Errorf("extracted %q", got)
	}
}

func TestExtractTarGzRejectsTraversal(t *testing.T) {
	dst := t.TempDir()
	src := filepath.Join(t.TempDir(), "evil.tar.gz")
	archive := makeTarGz(t, []tarEntry{{name: "../evil", body: "X"}})
	if err := os.WriteFile(src, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	err := extractTarGz(src, dst)
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("err = %v, want unsafe-path rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dst), "evil")); statErr == nil {
		t.Error("traversal file was written outside dst")
	}
}

func TestExtractTarGzBadGzip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "junk.tar.gz")
	if err := os.WriteFile(src, []byte("not gzip at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(src, t.TempDir()); err == nil {
		t.Fatal("garbage input accepted")
	}
	if err := extractTarGz(filepath.Join(t.TempDir(), "absent"), t.TempDir()); err == nil {
		t.Fatal("missing input accepted")
	}
}

// An entry larger than the extraction cap must fail loudly, not silently
// truncate — a truncated binary would pass the swap and brick the install
// (the archive checksum is verified, the EXTRACTED files are not).
func TestExtractTarGzRejectsOversizeEntry(t *testing.T) {
	oldLimit := extractLimit
	extractLimit = 16
	t.Cleanup(func() { extractLimit = oldLimit })
	dst := t.TempDir()
	src := filepath.Join(t.TempDir(), "big.tar.gz")
	archive := makeTarGz(t, []tarEntry{{name: "bin", body: strings.Repeat("A", 64)}})
	if err := os.WriteFile(src, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	err := extractTarGz(src, dst)
	if err == nil {
		got, _ := os.ReadFile(filepath.Join(dst, "bin"))
		t.Fatalf("oversize entry extracted silently truncated to %d bytes, want error", len(got))
	}
}

func TestMoveFileErrors(t *testing.T) {
	if err := moveFile(filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Error("moving a missing file succeeded")
	}
	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dst parent missing: rename fails, and so must the copy fallback.
	if err := moveFile(src, filepath.Join(t.TempDir(), "no", "such", "dir", "dst")); err == nil {
		t.Error("move into a missing directory succeeded")
	}
}

func TestMoveDirErrors(t *testing.T) {
	if err := moveDir(filepath.Join(t.TempDir(), "absent"), filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Error("moving a missing dir succeeded")
	}
}
