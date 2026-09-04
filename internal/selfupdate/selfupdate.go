// Package selfupdate applies an in-place update for any install whose tree is
// writable by the running user: it downloads the latest release for this
// OS/arch, verifies its SHA-256 against the release checksums, swaps the
// binary + web/ assets (keeping a backup for rollback), and re-execs. It is
// always triggered explicitly (never silently) and refuses to run on Windows
// (→ run the installer) or for a non-writable install — e.g. a .deb install
// whose postinstall left the tree root-owned — where a reinstall is the right
// path instead. A .deb install IS self-updatable once its tree is
// service-user-owned (ut-docs#151's `chown -R pos:pos /opt/unitill`).
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
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
)

// applyMu serializes Apply: the swap sequence below (rename exe→.bak, remove
// any stale .bak, move the new binary into place) has no atomicity across
// steps, so two concurrent callers — the manual "Update now" button and the
// unattended scheduler (ut-docs#79) now both call Apply — could interleave
// and delete each other's only backup mid-rename, leaving the install with no
// binary at all (ut-docs#79 review finding). A second caller fails fast
// instead of queuing, since queuing would just download+verify+swap again
// moments later for no benefit.
var applyMu sync.Mutex

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
	// goarch is a test seam for runtime.GOARCH, shared between Supported()
	// (gates whether "Update now" ever appears) and applyMacApp
	// (macapp_darwin.go, the click-time Intel refusal) so both use one
	// source of truth for "is this an Intel Mac" instead of two vars that
	// could drift apart. Declared here (not macapp_darwin.go) because
	// Supported() needs it on every platform's build, not just darwin's.
	goarch = runtime.GOARCH
)

// ErrUnsupported means this install type updates via a native mechanism.
var ErrUnsupported = errors.New("in-app update isn't available for this install; use the installer (Windows) or reinstall (this install's directory isn't self-update-writable)")

// Supported reports whether Apply can run for this build/install.
func Supported() bool {
	exe, err := osExecutable()
	if err != nil {
		return false
	}
	if !supportedFor(exe, runtime.GOOS) {
		return false
	}
	// A macOS .app updates the whole bundle via the .dmg (applyMacApp handles
	// its own privilege/relaunch path), so it isn't gated on plain
	// dir-writability. A *portable* darwin binary outside a bundle still does
	// the tar.gz swap below, so it needs a writable dir just like linux.
	if runtime.GOOS == "darwin" && appBundlePath(exe) != "" {
		return macAppBundleSupported(goarch)
	}
	// The real precondition for a portable unix install: Apply's swaps are
	// renames within (a) the binary's directory — for the binary + its .bak —
	// and (b) the server's working directory — for the on-disk web/ override,
	// which the server resolves cwd-relative, NOT next to the binary (the Pi
	// kiosk runs /opt/unitill/bin/unitill-pos with cwd=/opt/unitill and web at
	// /opt/unitill/web). Both must be writable by the service user, or Apply
	// would fail — or worse, half-complete — mid-swap. Checking this directly,
	// instead of guessing from the path, is what lets a service-writable
	// /opt/unitill kiosk install self-update while a root-owned one honestly
	// reports "no in-app update" rather than the old kiosk dead-end
	// (board ut-docs#147).
	if !dirWritable(filepath.Dir(exe)) {
		return false
	}
	if cwd, err := os.Getwd(); err == nil && cwd != filepath.Dir(exe) && !dirWritable(cwd) {
		return false
	}
	return true
}

// DownloadLinkActionable reports whether a user on this OS can get the new
// version themselves by clicking a website download link — the single
// source of truth shared by the Settings-page fallback
// (internal/pages/update_api.go's updateUnavailableHTML) and the
// status-bar chip (web/ui/layouts/base.html) so the two never drift into
// separately-maintained copies of the same check (ut-docs#159). Windows and
// macOS are windowed desktop OSes with a browser — actionable. A unix kiosk
// is fullscreen with no browser chrome, so a website link there is a dead
// end (ut-docs#147/#159): false.
func DownloadLinkActionable(goos string) bool {
	return goos == "windows" || goos == "darwin"
}

// DownloadLinkActionableNow is DownloadLinkActionable for the running
// process's own GOOS — the zero-arg form template funcs need (mirrors
// Supported()'s own zero-arg-wrapping-runtime.GOOS pattern below).
func DownloadLinkActionableNow() bool {
	return DownloadLinkActionable(runtime.GOOS)
}

// InstallBridgeAvailable reports whether the native shell around this build
// can drive the OS package installer on the operator's behalf. Today that is
// Android only: the Go core ships as a native library inside the APK and only
// the package installer may replace an app's own code, so Supported() is
// false there by design — but MainActivity.KioskBridge.installUpdate() can
// hand a downloaded APK to that installer, which is a real update path and
// not the dead end an unsupported unix kiosk has.
//
// One owner for the predicate, deliberately (ut-docs#1534 review): both the
// updateinstallbridge template func (via httpx.UpdateInstallBridge) and
// internal/pages/update_api.go's updateUnavailableHTML branch on it, and
// keeping the two "in step" by comment alone is exactly how the Settings page
// and the status chip drifted apart in the first place.
func InstallBridgeAvailable(goos string) bool {
	return goos == "android"
}

// InstallBridgeAvailableNow is InstallBridgeAvailable for the running
// process's own GOOS — the zero-arg form template funcs need, same shape as
// DownloadLinkActionableNow above.
func InstallBridgeAvailableNow() bool {
	return InstallBridgeAvailable(runtime.GOOS)
}

// supportedFor is the OS/location POLICY gate (pure, no filesystem access):
// which install shapes can *ever* self-update. The final writability
// precondition is applied by Supported(), not here.
func supportedFor(exe, goos string) bool {
	switch goos {
	case "windows":
		return false // updates via the installer
	case "android", "ios":
		// Neither has a real self-update mechanism. Apply() only ever fetches
		// a unitill-pos_<version>_<goos>_<arch>.tar.gz archive (or, on darwin,
		// a .dmg via applyMacApp); no such archive is published for
		// android/ios (their release job attaches an .apk, nothing a self-swap
		// can use), so "Update now" here would fail with a confusing "no
		// release archive" error. False makes the status bar fall back to the
		// plain download link instead (same as Windows). A real mobile
		// updater (fetch the .apk, launch the package-install intent) is a
		// feature to build later.
		return false
	case "darwin":
		// A macOS .app updates via the .dmg (whole-bundle replace + relaunch),
		// handled by applyMacApp — always location-eligible.
		return true
	}
	// apt/dpkg owns the /usr system prefix — never self-swap there (its
	// domain). /opt/unitill is NOT blocklisted: the Pi kiosk installs there as
	// the service user and self-updates fine; the old blanket /opt block was
	// the bug behind the kiosk dead-end (ut-docs#147). Whether this particular
	// install can actually be swapped is decided by dirWritable in Supported().
	if strings.HasPrefix(exe, "/usr/") {
		return false
	}
	return true
}

// dirWritable reports whether dir can be written by the current process — the
// precondition for the rename-based binary swap in Apply. It probes by
// creating and removing a temp file, which honours ownership, ACLs and
// read-only mounts more reliably than inspecting the mode bits. A non-existent
// or non-writable directory returns false.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".unitill-uptest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// macAppBundleSupported reports whether a macOS .app-bundle install can
// self-update via the .dmg whole-bundle replace (applyMacApp). Only arm64
// dmgs are ever published (.goreleaser.yaml + the macos-app release job both
// build arm64 only) — an Intel Mac must never see the "Update now" button,
// only the plain download-page chip (the same treatment Windows already
// gets, ut-docs#152), since clicking it would only ever fail at
// applyMacApp's own Intel refusal (macapp_darwin.go) with a confusing
// "no macOS .dmg" style error.
func macAppBundleSupported(arch string) bool {
	return arch == "arm64"
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
	if !applyMu.TryLock() {
		return errors.New("an update is already being applied")
	}
	defer applyMu.Unlock()

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
	// The server resolves its on-disk web/ override relative to its working
	// directory (registerStatic → filepath.Join("web","public")), which is NOT
	// necessarily the binary's directory: the Pi kiosk runs
	// /opt/unitill/bin/unitill-pos with WorkingDirectory=/opt/unitill and web
	// at /opt/unitill/web. Swap web/ where the server actually reads it, or a
	// self-update silently serves stale /public assets over the freshly-swapped
	// binary's newer embedded ones (ut-docs#148). For a flat portable install
	// cwd == installDir, so this is unchanged there.
	webBase := installDir
	if cwd, err := os.Getwd(); err == nil {
		webBase = cwd
	}

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
		curWeb := filepath.Join(webBase, "web")
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
