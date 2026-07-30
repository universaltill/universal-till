// Package selfupdate applies an in-place update for archive (tar.gz) installs:
// it downloads the latest release for this OS/arch, verifies its SHA-256
// against the release checksums, swaps the binary + web/ assets (keeping a
// backup for rollback), and re-execs. It is always triggered explicitly (never
// silently) and refuses to run for packaged installs (.deb → apt) or Windows
// (→ run the installer), where a native updater is the right path.
//
// Data lives in the stable data dir (internal/paths), separate from the install
// dir, so a swap never touches the shop's database.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
)

// releasesLatest is a var (not const) purely as a test seam: tests point it at
// a local httptest server so no test ever talks to the real GitHub API.
var releasesLatest = "https://api.github.com/repos/universaltill/universal-till/releases/latest"

// Test seams. Production code never changes these; tests do, so that the full
// Apply flow can run hermetically against a temp-dir "install" without ever
// touching the real running binary or re-exec'ing the test process (a real
// syscall.Exec would replace the test binary mid-run).
var (
	osExecutable = os.Executable
	reexecFn     = reexec
	reexecDelay  = 1500 * time.Millisecond
)

// ErrUnsupported means this install type updates via a native mechanism.
var ErrUnsupported = errors.New("in-app update is only for archive (.tar.gz) installs; use the installer (Windows) or apt (.deb)")

// Supported reports whether Apply can run for this build/install.
func Supported() bool {
	exe, err := osExecutable()
	if err != nil {
		return false
	}
	return supportedFor(exe, runtime.GOOS)
}

// supportedFor is the testable core of Supported: in-app self-update only works
// for the portable archive (.tar.gz) installs, where swapping the binary in
// place is safe.
func supportedFor(exe, goos string) bool {
	if goos == "windows" {
		return false // updates via the installer
	}
	if goos == "android" || goos == "ios" {
		// Neither had a real self-update mechanism to begin with — this was
		// falling through to the desktop default (true) with nothing to
		// back it up. Apply() only ever fetches a
		// unitill-pos_<version>_<goos>_<arch>.tar.gz release archive (or,
		// on darwin, a .dmg via applyMacApp); no such archive is ever
		// published for android/ios (mobile/mobile.go's release job
		// attaches an .apk, nothing a self-swap could use), so tapping
		// "Update now" here was guaranteed to fail with a confusing "no
		// release archive" error — same user-visible symptom as the
		// stale-cache Mac bug fixed alongside this, different root cause
		// (a feature that was never actually built for this platform, not
		// a timing bug). False here makes the status bar correctly fall
		// back to the plain "Update available" download-page link instead
		// (same fallback Windows already uses). A real mobile in-app
		// updater (download the .apk, launch Android's package-install
		// intent) is a genuine feature to build later, not implemented by
		// this change.
		return false
	}
	// Packaged installs update via their package manager, not a self-swap.
	for _, p := range []string{"/usr/", "/opt/unitill/"} {
		if strings.HasPrefix(exe, p) {
			return false // .deb → apt
		}
	}
	// A macOS .app updates via the .dmg (whole-bundle replace + relaunch), not by
	// swapping the inner binary — handled by applyMacApp. Still supported.
	return true
}

// appBundlePath returns the path of the enclosing macOS .app bundle when the
// executable runs inside one (…/Something.app/Contents/MacOS/binary), else "".
func appBundlePath(exe string) string {
	const marker = ".app/Contents/MacOS/"
	if i := strings.Index(exe, marker); i != -1 {
		return exe[:i+len(".app")]
	}
	return ""
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// Apply downloads the latest release, verifies it, swaps the binary + web/, and
// schedules a re-exec of the new binary. It returns after the swap is staged;
// the process re-execs a moment later so the caller's HTTP response can flush.
func Apply(ctx context.Context) error {
	log := logging.L()
	if !Supported() {
		return ErrUnsupported
	}
	exe, err := osExecutable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}

	// macOS .app: replace the whole bundle from the .dmg and relaunch, instead
	// of swapping the inner (signed) binary with an unsigned archive one.
	if runtime.GOOS == "darwin" {
		if app := appBundlePath(exe); app != "" {
			return applyMacApp(ctx, app)
		}
	}
	installDir := filepath.Dir(exe)

	rel, err := fetchLatest(ctx)
	if err != nil {
		return err
	}
	version := strings.TrimPrefix(rel.TagName, "v")

	archiveName := fmt.Sprintf("unitill-pos_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	archiveURL, checksumsURL := "", ""
	for _, a := range rel.Assets {
		switch {
		case a.Name == archiveName:
			archiveURL = a.URL
		case a.Name == "checksums.txt":
			checksumsURL = a.URL
		}
	}
	if archiveURL == "" {
		return fmt.Errorf("no release archive for %s/%s (%s)", runtime.GOOS, runtime.GOARCH, archiveName)
	}

	// Download + verify the archive.
	tmp, err := os.MkdirTemp("", "ut-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	archivePath := filepath.Join(tmp, archiveName)
	if err := download(ctx, archiveURL, archivePath); err != nil {
		return fmt.Errorf("download: %w", err)
	}
	// Fail closed: every real release ships checksums.txt (.goreleaser.yaml
	// name_template), so a release without one only ever means a broken or
	// tampered release — never install unverified.
	if checksumsURL == "" {
		return fmt.Errorf("release v%s has no checksums.txt — refusing to install an unverified update", version)
	}
	want, err := checksumFor(ctx, checksumsURL, archiveName)
	if err != nil {
		return err
	}
	if err := verifySHA256(archivePath, want); err != nil {
		return err
	}

	// Extract to a staging dir (contains the new binary + web/).
	stage := filepath.Join(tmp, "stage")
	if err := extractTarGz(archivePath, stage); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	newBin := filepath.Join(stage, filepath.Base(exe))
	newWeb := filepath.Join(stage, "web")
	if _, err := os.Stat(newBin); err != nil {
		return fmt.Errorf("update archive missing %s", filepath.Base(exe))
	}

	// Swap the binary (rename = atomic on the same fs; keep a .bak for rollback).
	bak := exe + ".bak"
	_ = os.Remove(bak)
	if err := os.Rename(exe, bak); err != nil {
		return fmt.Errorf("back up current binary: %w", err)
	}
	if err := moveFile(newBin, exe); err != nil {
		_ = os.Rename(bak, exe) // rollback
		return fmt.Errorf("install new binary: %w", err)
	}
	_ = os.Chmod(exe, 0o755)

	// Swap web/ if the archive shipped it (keep a backup, best-effort).
	if _, err := os.Stat(newWeb); err == nil {
		curWeb := filepath.Join(installDir, "web")
		webBak := curWeb + ".bak"
		_ = os.RemoveAll(webBak)
		if _, err := os.Stat(curWeb); err == nil {
			_ = os.Rename(curWeb, webBak)
		}
		if err := moveDir(newWeb, curWeb); err != nil {
			// Roll web back; the binary is new but web mismatched — restore old.
			_ = os.RemoveAll(curWeb)
			_ = os.Rename(webBak, curWeb)
			_ = os.Remove(exe)
			_ = os.Rename(bak, exe)
			return fmt.Errorf("install new web assets: %w", err)
		}
	}

	log.Infof("[selfupdate] updated to v%s — restarting", version)
	// Re-exec the new binary shortly, so the HTTP response can flush first.
	go func() {
		time.Sleep(reexecDelay)
		if err := reexecFn(exe); err != nil {
			logging.L().Errorf("[selfupdate] re-exec failed (restart manually): %v", err)
		}
	}()
	return nil
}

// LatestVersionURL and related helpers -------------------------------------

func fetchLatest(ctx context.Context) (*ghRelease, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, releasesLatest, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("releases API: HTTP %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if strings.TrimPrefix(rel.TagName, "v") == buildinfo.Version {
		return nil, errors.New("already on the latest version")
	}
	return &rel, nil
}

func download(ctx context.Context, url, dst string) error {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func checksumFor(ctx context.Context, url, name string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksum not found for %s", name)
}

func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch: got %s want %s", got, want)
	}
	return nil
}

// extractLimit caps how many bytes a single archive entry may extract to
// (zip-bomb guard). A var so tests can exercise the limit without writing
// half-gigabyte fixtures.
var extractLimit int64 = 512 << 20

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Guard against path traversal.
		target := filepath.Join(dst, hdr.Name)
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			// Read one byte past the cap so an over-limit entry fails loudly
			// instead of silently truncating (a truncated binary would pass
			// the swap — the archive checksum covers the .tar.gz, not the
			// extracted files).
			n, err := io.Copy(out, io.LimitReader(tr, extractLimit+1))
			out.Close()
			if err != nil {
				return err
			}
			if n > extractLimit {
				return fmt.Errorf("archive entry %s exceeds the %d-byte extraction limit", hdr.Name, extractLimit)
			}
		}
	}
}

// moveFile / moveDir fall back to copy+remove across filesystems.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func moveDir(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return moveFile(p, target)
	})
}
