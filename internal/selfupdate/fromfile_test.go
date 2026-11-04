package selfupdate

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// ut-docs#5438: install from a file. Like the other install tests these
// mutate package seams and must not use t.Parallel.

// signedSet builds an archive for version v with its checksums.txt and a
// valid signature by key seed 1 (callers arm sigPubHex(1)).
func signedSet(t *testing.T, v string) (name string, archive, sums, sig []byte) {
	t.Helper()
	name = archiveNameFor(v)
	archive = makeTarGz(t, []tarEntry{
		{name: "unitill-pos", body: "NEW-BINARY-v" + v},
		{name: "web", dir: true},
		{name: "web/index.html", body: "NEW-WEB"},
	})
	sums = goodSums(name, archive)
	sig = []byte(testSign(sigSeed(1), sums) + "\n")
	return
}

func stageSet(t *testing.T, fi *FileInstall, name string, archive, sums, sig []byte) FileSet {
	t.Helper()
	p := filepath.Join(fi.Dir(), name)
	if err := os.WriteFile(p, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	return FileSet{ArchiveName: name, ArchivePath: p, Sums: sums, Sig: sig}
}

// noNetwork points the release API at a server that fails the test on any hit.
func noNetwork(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("file install touched the network: %s", r.URL)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	old := releasesLatest
	releasesLatest = srv.URL
	t.Cleanup(func() { releasesLatest = old })
}

func assertUntouched(t *testing.T, fx *installFixture, fi *FileInstall) {
	t.Helper()
	if got, _ := os.ReadFile(fx.exe); string(got) != fx.oldBin {
		t.Errorf("binary changed: %q", got)
	}
	if _, err := os.Stat(fx.exe + ".bak"); !os.IsNotExist(err) {
		t.Errorf("unexpected .bak: %v", err)
	}
	if _, ok := readPending(fx.dir); ok {
		t.Error("unexpected pending marker")
	}
	if got, _ := os.ReadFile(filepath.Join(fx.dir, "web", "index.html")); string(got) != "OLD-WEB" {
		t.Errorf("web changed: %q", got)
	}
	dir := fi.Dir()
	fi.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir %s not removed: %v", dir, err)
	}
}

func beginT(t *testing.T) *FileInstall {
	t.Helper()
	fi, err := BeginFileInstall()
	if err != nil {
		t.Fatalf("BeginFileInstall: %v", err)
	}
	t.Cleanup(fi.Close)
	return fi
}

func TestFileInstall_GenuineNewerInstalls(t *testing.T) {
	fx := newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	noNetwork(t)
	name, archive, sums, sig := signedSet(t, "0.2.0")
	fi := beginT(t)
	got, err := fi.Install(context.Background(), stageSet(t, fi, name, archive, sums, sig))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got.Version != "0.2.0" || got.KeyID == "" {
		t.Errorf("Installed = %+v", got)
	}
	if b, _ := os.ReadFile(fx.exe); string(b) != "NEW-BINARY-v0.2.0" {
		t.Errorf("binary not swapped: %q", b)
	}
	if p, ok := readPending(fx.dir); !ok || p.Version != "0.2.0" {
		t.Errorf("pending = %+v %v", p, ok)
	}
}

func TestFileInstall_TamperedChecksumsByte(t *testing.T) {
	fx := newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	name, archive, sums, sig := signedSet(t, "0.2.0")
	sums = append([]byte{}, sums...)
	sums[0] ^= 1
	fi := beginT(t)
	_, err := fi.Install(context.Background(), stageSet(t, fi, name, archive, sums, sig))
	if !errors.Is(err, ErrSignature) {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
	assertUntouched(t, fx, fi)
}

func TestFileInstall_SigMissingEmptyMalformedUnknownKey(t *testing.T) {
	name, archive, sums, _ := signedSet(t, "0.2.0")
	cases := map[string][]byte{
		"missing":     nil,
		"empty":       []byte(""),
		"malformed":   []byte("garbage\n"),
		"unknown key": []byte(testSign(sigSeed(9), sums) + "\n"),
	}
	for label, sig := range cases {
		t.Run(label, func(t *testing.T) {
			fx := newInstallFixture(t)
			armKeys(t, sigPubHex(1))
			fi := beginT(t)
			_, err := fi.Install(context.Background(), stageSet(t, fi, name, archive, sums, sig))
			if !errors.Is(err, ErrSignature) {
				t.Fatalf("err = %v, want ErrSignature", err)
			}
			assertUntouched(t, fx, fi)
		})
	}
}

func TestFileInstall_SigOverOtherReleaseSums(t *testing.T) {
	fx := newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	_, _, sums, sig := signedSet(t, "0.2.0") // genuine, but for v0.2.0
	name3, archive3, _, _ := signedSet(t, "0.3.0")
	fi := beginT(t)
	_, err := fi.Install(context.Background(), stageSet(t, fi, name3, archive3, sums, sig))
	if !errors.Is(err, ErrFileChecksum) {
		t.Fatalf("err = %v, want ErrFileChecksum", err)
	}
	assertUntouched(t, fx, fi)
}

func TestFileInstall_TamperedArchive(t *testing.T) {
	fx := newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	name, archive, sums, sig := signedSet(t, "0.2.0")
	archive = append(append([]byte{}, archive...), 'x')
	fi := beginT(t)
	_, err := fi.Install(context.Background(), stageSet(t, fi, name, archive, sums, sig))
	if !errors.Is(err, ErrFileChecksum) {
		t.Fatalf("err = %v, want ErrFileChecksum", err)
	}
	assertUntouched(t, fx, fi)
}

func TestFileInstall_OldArchiveRenamedNewer(t *testing.T) {
	fx := newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	_, oldArchive, _, _ := signedSet(t, "0.1.5") // genuine older bytes
	name, _, sums, sig := signedSet(t, "0.2.0")  // genuine signed sums for the newer name
	fi := beginT(t)
	_, err := fi.Install(context.Background(), stageSet(t, fi, name, oldArchive, sums, sig))
	if !errors.Is(err, ErrFileChecksum) {
		t.Fatalf("err = %v, want ErrFileChecksum", err)
	}
	assertUntouched(t, fx, fi)
}

func TestFileArchiveVersion(t *testing.T) {
	newInstallFixture(t) // buildinfo.Version = 0.1.0
	good := archiveNameFor("0.2.0")
	if v, err := FileArchiveVersion(good); err != nil || v != "0.2.0" {
		t.Fatalf("good = %q, %v", v, err)
	}
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "windows"
	}
	otherArch := "arm64"
	if runtime.GOARCH == "arm64" {
		otherArch = "amd64"
	}
	suf := "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	cases := []struct {
		name string
		want error
	}{
		{archiveNameFor("0.0.9"), ErrFileNotNewer},
		{archiveNameFor("0.1.0"), ErrFileSameVersion},
		{"unitill-pos_0.2.0_" + other + "_" + runtime.GOARCH + ".tar.gz", ErrFileBadName},
		{"unitill-pos_0.2.0_" + runtime.GOOS + "_" + otherArch + ".tar.gz", ErrFileBadName},
		{"unitill-pos_0.2.0" + suf + " (1)", ErrFileBadName},
		{"unitill-pos_0.2.0 (1)" + suf, ErrFileBadName},
		{"../unitill-pos_0.2.0" + suf, ErrFileBadName},
		{"/tmp/unitill-pos_0.2.0" + suf, ErrFileBadName},
		{"unitill-pos_0.2.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip", ErrFileBadName},
		{"unitill-pos_0.2.0.dmg", ErrFileBadName},
		{"unitill-pos_0.2.0-rc1" + suf, ErrFileBadName},
		{"", ErrFileBadName},
	}
	for _, c := range cases {
		if _, err := FileArchiveVersion(c.name); !errors.Is(err, c.want) {
			t.Errorf("%q: err = %v, want %v", c.name, err, c.want)
		}
	}
	if !strings.HasSuffix(good, FileArchiveSuffix()) {
		t.Errorf("suffix %q not a suffix of %q", FileArchiveSuffix(), good)
	}
}

func TestFileInstallAvailability_Unarmed(t *testing.T) {
	newInstallFixture(t)
	armKeys(t, "")
	if err := FileInstallAvailability(); !errors.Is(err, ErrFileNotArmed) {
		t.Fatalf("availability = %v, want ErrFileNotArmed", err)
	}
	tmpBefore, _ := filepath.Glob(filepath.Join(os.TempDir(), "ut-update-file-*"))
	fi, err := BeginFileInstall()
	if fi != nil || !errors.Is(err, ErrFileNotArmed) {
		t.Fatalf("Begin = %v, %v", fi, err)
	}
	tmpAfter, _ := filepath.Glob(filepath.Join(os.TempDir(), "ut-update-file-*"))
	if len(tmpAfter) != len(tmpBefore) {
		t.Error("a temp dir was created for an unarmed build")
	}
	if !applyMu.TryLock() {
		t.Fatal("applyMu left held")
	}
	applyMu.Unlock()
}

func TestFileInstallAvailability_UnusableStamp(t *testing.T) {
	newInstallFixture(t)
	armKeys(t, "zz")
	err := FileInstallAvailability()
	if !errors.Is(err, ErrFileNotArmed) || !errors.Is(err, ErrSignature) {
		t.Fatalf("err = %v, want both ErrFileNotArmed and ErrSignature", err)
	}
}

func TestFileInstallAvailability_WindowsSetupAndMacApp(t *testing.T) {
	armKeys(t, sigPubHex(1))
	oldExe, oldGOOS, oldArch := osExecutable, hostGOOS, goarch
	t.Cleanup(func() { osExecutable, hostGOOS, goarch = oldExe, oldGOOS, oldArch })

	// Windows setup install (uninstall.exe next to the exe).
	dir := t.TempDir()
	exe := filepath.Join(dir, "unitill-pos.exe")
	for _, f := range []string{exe, filepath.Join(dir, windowsUninstaller)} {
		if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	osExecutable = func() (string, error) { return exe, nil }
	hostGOOS = "windows"
	if err := FileInstallAvailability(); !errors.Is(err, ErrFileUnsupported) {
		t.Errorf("windows setup = %v, want ErrFileUnsupported", err)
	}

	// Portable Windows zip: not self-updatable at all.
	zipDir := t.TempDir()
	zipExe := filepath.Join(zipDir, "unitill-pos.exe")
	_ = os.WriteFile(zipExe, []byte("x"), 0o755)
	osExecutable = func() (string, error) { return zipExe, nil }
	if err := FileInstallAvailability(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("windows zip = %v, want ErrUnsupported", err)
	}

	// macOS .app.
	hostGOOS, goarch = "darwin", "arm64"
	osExecutable = func() (string, error) { return "/Applications/Universal Till.app/Contents/MacOS/unitill-pos", nil }
	if err := FileInstallAvailability(); !errors.Is(err, ErrFileUnsupported) {
		t.Errorf("mac app = %v, want ErrFileUnsupported", err)
	}

	// apt / .deb.
	hostGOOS = "linux"
	osExecutable = func() (string, error) { return "/usr/bin/unitill-pos", nil }
	if err := FileInstallAvailability(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("apt = %v, want ErrUnsupported", err)
	}
}

// UX R-UX4: when several reasons apply, the one that survives arming wins.
func TestFileInstallAvailability_ReasonOrder(t *testing.T) {
	armKeys(t, "") // also unarmed
	oldExe := osExecutable
	t.Cleanup(func() { osExecutable = oldExe })
	osExecutable = func() (string, error) { return "/usr/bin/unitill-pos", nil }
	if err := FileInstallAvailability(); !errors.Is(err, ErrUnsupported) || errors.Is(err, ErrFileNotArmed) {
		t.Fatalf("err = %v, want ErrUnsupported (not not_armed)", err)
	}
}

func TestFileInstall_BusyBothWays(t *testing.T) {
	newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	fi, err := BeginFileInstall()
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyVersion(context.Background(), "9.9.9"); !errors.Is(err, ErrBusy) {
		t.Errorf("ApplyVersion while file install open = %v, want ErrBusy", err)
	}
	if _, err := BeginFileInstall(); !errors.Is(err, ErrBusy) {
		t.Errorf("second Begin = %v, want ErrBusy", err)
	}
	fi.Close()
	fi.Close() // idempotent

	applyMu.Lock()
	_, err = BeginFileInstall()
	applyMu.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Errorf("Begin while online update holds applyMu = %v, want ErrBusy", err)
	}
}

func TestFileInstall_SmokeFailRollsBack(t *testing.T) {
	fx := newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	stubSmokeRun(t, errors.New("exec format error"))
	name, archive, sums, sig := signedSet(t, "0.2.0")
	fi := beginT(t)
	_, err := fi.Install(context.Background(), stageSet(t, fi, name, archive, sums, sig))
	if !errors.Is(err, ErrCannotStart) {
		t.Fatalf("err = %v, want ErrCannotStart", err)
	}
	if !strings.Contains(err.Error(), "could not start") || !strings.Contains(err.Error(), "0.1.0") {
		t.Errorf("message changed: %v", err)
	}
	if b, _ := os.ReadFile(fx.exe); string(b) != fx.oldBin {
		t.Errorf("old binary not restored: %q", b)
	}
	if _, ok := readPending(fx.dir); ok {
		t.Error("pending marker written for a failed update")
	}
}

func TestFileInstall_ArchivePathOutsideDir(t *testing.T) {
	fx := newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	name, archive, sums, sig := signedSet(t, "0.2.0")
	outside := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(outside, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	fi := beginT(t)
	_, err := fi.Install(context.Background(), FileSet{ArchiveName: name, ArchivePath: outside, Sums: sums, Sig: sig})
	if err == nil {
		t.Fatal("archive outside the private dir was accepted")
	}
	assertUntouched(t, fx, fi)
}

func TestFileInstall_ArchiveCap(t *testing.T) {
	newInstallFixture(t)
	armKeys(t, sigPubHex(1))
	fi := beginT(t)
	old := fileArchiveCap
	t.Cleanup(func() { fileArchiveCap = old })
	fileArchiveCap = func(string) int64 { return 1234 }
	if got := fi.ArchiveCap(); got != 1234 {
		t.Errorf("ArchiveCap = %d", got)
	}
}

func TestFileInstall_NotNewerRefusedBeforeSignature(t *testing.T) {
	fx := newInstallFixture(t)
	buildinfo.Version = "0.5.0"
	armKeys(t, sigPubHex(1))
	name, archive, sums, sig := signedSet(t, "0.2.0")
	fi := beginT(t)
	_, err := fi.Install(context.Background(), stageSet(t, fi, name, archive, sums, sig))
	if !errors.Is(err, ErrFileNotNewer) {
		t.Fatalf("err = %v, want ErrFileNotNewer", err)
	}
	assertUntouched(t, fx, fi)
}

// AC2: one release-signature verifier. Exactly one releasesig.Verify call in
// the package's non-test sources.
func TestOneReleasesigVerifyCall(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Verify" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "releasesig" {
						n++
					}
				}
				return true
			})
		}
	}
	if n != 1 {
		t.Fatalf("releasesig.Verify called %d times in internal/selfupdate, want exactly 1", n)
	}
}
