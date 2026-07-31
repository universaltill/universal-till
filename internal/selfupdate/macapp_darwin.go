//go:build darwin

package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/universaltill/universal-till/internal/logging"
)

// applyMacApp updates a running macOS .app in place: it downloads the new
// .dmg, verifies its SHA-256 against the release's checksums.txt (same
// fail-closed contract as the archive path in selfupdate.go), copies the new
// (ad-hoc-signed) .app out of it, checks the signature, then launches a
// detached helper that quits this app, replaces the bundle in /Applications,
// and relaunches the new version. Unlike the archive path it never swaps the
// inner binary (that would break the code signature and the release archive
// binary is unsigned anyway). Only arm64 is supported — no Intel .dmg is
// ever published (see the goarch check below).
func applyMacApp(ctx context.Context, appPath string) error {
	log := logging.L()
	// Belt-and-suspenders: Supported() (selfupdate.go) already hides the
	// "Update now" button for an Intel Mac via this same goarch var
	// (ut-docs#18), so Apply() should never route here for one in practice.
	// Kept as this function's own guard anyway — it's the only thing
	// standing between a future caller that bypasses Supported() and a
	// wasted fetch. .goreleaser.yaml builds darwin/arm64 only, and the
	// macos-app release job (.github/workflows/release.yml) only ever
	// builds/attaches an arm64 dmg — there is no Intel .dmg to fetch.
	if goarch != "arm64" {
		return fmt.Errorf("in-app update is not available for Intel Macs — download the latest .dmg from https://www.universaltill.com/download instead")
	}
	rel, err := fetchLatest(ctx)
	if err != nil {
		return err
	}
	version := strings.TrimPrefix(rel.TagName, "v")
	dmgName := fmt.Sprintf("unitill-pos-%s-macOS-arm64.dmg", version)
	dmgURL, checksumsURL := "", ""
	for _, a := range rel.Assets {
		switch {
		case a.Name == dmgName:
			dmgURL = a.URL
		case a.Name == "checksums.txt":
			checksumsURL = a.URL
		}
	}
	if dmgURL == "" {
		return fmt.Errorf("no macOS .dmg in release v%s", version)
	}

	// A work dir that OUTLIVES this process — the helper runs after we quit.
	work, err := os.MkdirTemp("", "ut-macupdate-*")
	if err != nil {
		return err
	}
	cleanupOnErr := func(e error) error { _ = os.RemoveAll(work); return e }

	dmgPath := filepath.Join(work, dmgName)
	if err := download(ctx, dmgURL, dmgPath); err != nil {
		return cleanupOnErr(fmt.Errorf("download .dmg: %w", err))
	}

	// Integrity gate #1: verify the downloaded .dmg against the release's
	// checksums.txt before ever mounting it — codesign below only proves
	// internal signature consistency (a tampered ad-hoc-signed bundle still
	// passes it), so this is the only check that catches a swapped-out
	// download. Fail closed exactly like the archive path (selfupdate.go's
	// Apply): every real release ships checksums.txt with the dmg's line
	// appended by the macos-app release job, so a missing entry only ever
	// means a broken or tampered release.
	if checksumsURL == "" {
		return cleanupOnErr(fmt.Errorf("release v%s has no checksums.txt — refusing to install an unverified update", version))
	}
	want, err := checksumFor(ctx, checksumsURL, dmgName)
	if err != nil {
		return cleanupOnErr(err)
	}
	if err := verifySHA256(dmgPath, want); err != nil {
		return cleanupOnErr(err)
	}

	// Mount, copy the app out, detach.
	mnt := filepath.Join(work, "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return cleanupOnErr(err)
	}
	if out, err := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-quiet", "-mountpoint", mnt, dmgPath).CombinedOutput(); err != nil {
		return cleanupOnErr(fmt.Errorf("mount .dmg: %v: %s", err, strings.TrimSpace(string(out))))
	}
	staged := filepath.Join(work, "Universal Till.app")
	dittoErr := exec.CommandContext(ctx, "ditto", filepath.Join(mnt, "Universal Till.app"), staged).Run()
	_ = exec.Command("hdiutil", "detach", "-quiet", "-force", mnt).Run()
	if dittoErr != nil {
		return cleanupOnErr(fmt.Errorf("copy app out of .dmg: %w", dittoErr))
	}

	// Integrity gate #2: the staged app must have a valid (ad-hoc) signature.
	// This alone was the ONLY check before the checksum gate above — it only
	// proves internal signature consistency, so a tampered ad-hoc-signed
	// bundle would still pass it; the checksum gate is what actually catches
	// a swapped-out download.
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", staged).CombinedOutput(); err != nil {
		return cleanupOnErr(fmt.Errorf("downloaded app failed signature check: %v: %s", err, strings.TrimSpace(string(out))))
	}

	// Launch the detached replace-and-relaunch helper.
	helper := filepath.Join(work, "ut-updater.sh")
	logPath := filepath.Join(work, "updater.log")
	if err := os.WriteFile(helper, []byte(macUpdaterScript), 0o755); err != nil {
		return cleanupOnErr(err)
	}
	cmd := exec.Command("/bin/bash", helper, staged, appPath, logPath, work)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // survive our exit
	if err := cmd.Start(); err != nil {
		return cleanupOnErr(fmt.Errorf("launch updater: %w", err))
	}
	_ = cmd.Process.Release()
	log.Infof("[selfupdate] mac update to v%s staged; the app will restart", version)
	return nil
}

// macUpdaterScript quits the running app, swaps the bundle (with a restore on
// failure), clears quarantine, relaunches, and cleans up. Args: NEW APP LOG WORK.
const macUpdaterScript = `#!/bin/bash
NEW="$1"; APP="$2"; LOG="$3"; WORK="$4"
exec >>"$LOG" 2>&1
echo "$(date) updater start new=$NEW app=$APP"
osascript -e 'quit app "Universal Till"' 2>/dev/null || true
for i in $(seq 1 30); do
  pgrep -x unitill-desktop >/dev/null 2>&1 || pgrep -x unitill-pos >/dev/null 2>&1 || break
  pkill -x unitill-desktop 2>/dev/null || true
  pkill -x unitill-pos 2>/dev/null || true
  sleep 0.5
done
BAK="${APP}.old"
rm -rf "$BAK"
mv "$APP" "$BAK" 2>/dev/null || true
if ditto "$NEW" "$APP"; then
  xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true
  rm -rf "$BAK"
  echo "$(date) install ok"
else
  echo "$(date) install FAILED — restoring previous app"
  rm -rf "$APP"
  mv "$BAK" "$APP" 2>/dev/null || true
fi
open "$APP"
sleep 3
rm -rf "$WORK"
`
