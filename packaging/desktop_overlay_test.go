package packaging

// Regression guards for the desktop-OS kiosk-overlay install branch
// (ut-docs#1040): a fresh .deb install on a Pi running a DESKTOP OS — the
// exact case is_pi_appliance deliberately bails on — must stage the till as
// a fullscreen kiosk ON TOP of that desktop (seed WindowMode=kiosk +
// LaunchOnStartup=true via `unitill-pos provision-desktop-kiosk-defaults`,
// stage the login user's XDG autostart entry via `unitill-desktop
// --install-autostart`), while the headless-appliance branch stays exactly
// as it was and the two can never both fire for one install.
//
// Same testing conventions as kiosk_setup_test.go (see its file header):
// asserts operate on NON-COMMENT lines, and the display-manager split the
// two branches differ on is exercised by actually RUNNING the extracted
// shell functions against a fake systemctl — a raw substring check proved
// revertible-in-comments in that file's own history.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// overlayPredicateResult extracts has_real_display_manager plus both branch
// predicates from postinstall.sh, stubs the shared environmental gate
// (dpkg $1/$2, /run/systemd, /proc/device-tree/model, /etc/os-release —
// none of which can exist in a test sandbox), and runs the named predicate
// against a fake systemctl reporting the given display-manager Id/LoadState.
func overlayPredicateResult(t *testing.T, script, fn, id, loadState string) bool {
	t.Helper()
	bin := t.TempDir()
	systemctl := filepath.Join(bin, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\nprintf '%s\\n%s\\n' \"$UT_SYSTEMD_ID\" \"$UT_SYSTEMD_LOAD_STATE\"\n"), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	harness := shellFunction(t, script, "has_real_display_manager") + "\n" +
		shellFunction(t, script, "is_fresh_install_pi_debian") + "\n" +
		shellFunction(t, script, "is_pi_appliance") + "\n" +
		shellFunction(t, script, "is_desktop_kiosk_overlay") + "\n" +
		// Isolate the display-manager/opt-out split: the shared gate's
		// environmental checks are all satisfied on a real fresh Pi install.
		"is_fresh_install_pi_debian() { return 0; }\n" +
		"if " + fn + ` configure ""; then exit 0; fi; exit 1`
	cmd := exec.Command("sh", "-c", harness)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "UT_SYSTEMD_ID="+id, "UT_SYSTEMD_LOAD_STATE="+loadState)
	return cmd.Run() == nil
}

// TestDesktopOverlayAndApplianceBranchesAreMutuallyExclusive proves the two
// auto-setup branches split exactly on the resolved display manager and can
// never both fire: a loaded real DM service selects the overlay branch, and
// everything else (alias to a target, not-found) selects the appliance
// branch — never both, never with the roles swapped.
func TestDesktopOverlayAndApplianceBranchesAreMutuallyExclusive(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")

	cases := []struct {
		name, id, loadState        string
		wantAppliance, wantOverlay bool
	}{
		{"real desktop DM (lightdm)", "lightdm.service", "loaded", false, true},
		{"alias to graphical.target", "graphical.target", "loaded", true, false},
		{"no DM at all", "display-manager.service", "not-found", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			appliance := overlayPredicateResult(t, post, "is_pi_appliance", c.id, c.loadState)
			overlay := overlayPredicateResult(t, post, "is_desktop_kiosk_overlay", c.id, c.loadState)
			if appliance && overlay {
				t.Fatal("both branches fire for the same install — they must be mutually exclusive")
			}
			if appliance != c.wantAppliance {
				t.Errorf("is_pi_appliance = %v, want %v", appliance, c.wantAppliance)
			}
			if overlay != c.wantOverlay {
				t.Errorf("is_desktop_kiosk_overlay = %v, want %v", overlay, c.wantOverlay)
			}
		})
	}
}

// TestDesktopOverlayBranchGates asserts the overlay predicate carries every
// review-mandated gate as real code, sharing the fresh-install/Pi/Debian
// gate with the appliance branch (one author for the environment checks, so
// the two can never drift into overlapping) and adding its own distinct
// opt-out marker.
func TestDesktopOverlayBranchGates(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")

	shared := codeLines(shellFunction(t, post, "is_fresh_install_pi_debian"))
	if !anyLineContains(shared, `[ "$1" = "configure" ]`) {
		t.Error("is_fresh_install_pi_debian lost the dpkg configure gate")
	}
	if !anyLineContains(shared, `[ -z "$2" ]`) {
		t.Error("is_fresh_install_pi_debian lost the upgrade guard ($2 empty = fresh install) — an upgrade must never auto-stage either branch")
	}
	if !anyLineContains(shared, `"Raspberry Pi"`, "/proc/device-tree/model") {
		t.Error("is_fresh_install_pi_debian lost the Raspberry Pi model gate")
	}
	if !anyLineContains(shared, "ID=(debian|raspbian)") {
		t.Error("is_fresh_install_pi_debian lost the Debian-family os-release gate")
	}

	appliance := codeLines(shellFunction(t, post, "is_pi_appliance"))
	overlay := codeLines(shellFunction(t, post, "is_desktop_kiosk_overlay"))
	if !anyLineContains(appliance, "is_fresh_install_pi_debian") {
		t.Error("is_pi_appliance no longer routes through the shared fresh-install gate")
	}
	if !anyLineContains(overlay, "is_fresh_install_pi_debian") {
		t.Error("is_desktop_kiosk_overlay no longer routes through the shared fresh-install gate")
	}

	// The split itself, as code: appliance bails ON a real DM, overlay
	// REQUIRES one.
	if !anyLineContains(appliance, "has_real_display_manager") {
		t.Error("is_pi_appliance no longer consults has_real_display_manager")
	}
	if !anyLineContains(overlay, "has_real_display_manager", "return 1") {
		t.Error("is_desktop_kiosk_overlay no longer requires a real display manager as a code line")
	}

	// Its own opt-out marker — NOT the headless path's /etc/unitill/no-kiosk,
	// which already means "never kiosk-ify this box's console".
	if !anyLineContains(overlay, "/etc/unitill/no-desktop-kiosk-overlay") {
		t.Error("is_desktop_kiosk_overlay lost its /etc/unitill/no-desktop-kiosk-overlay opt-out gate")
	}
	for _, l := range overlay {
		if strings.Contains(l, "/etc/unitill/no-kiosk") && !strings.Contains(l, "no-desktop-kiosk-overlay") {
			t.Errorf("is_desktop_kiosk_overlay reuses the headless opt-out marker /etc/unitill/no-kiosk: %q", l)
		}
	}
}

// overlayBranchBlock extracts the `if is_desktop_kiosk_overlay ...; then`
// block's body (up to its matching top-level fi), comments stripped.
func overlayBranchBlock(t *testing.T, script string) []string {
	t.Helper()
	lines := strings.Split(script, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "if is_desktop_kiosk_overlay") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("postinstall.sh: no `if is_desktop_kiosk_overlay ...` branch found")
	}
	depth := 0
	var body []string
	for _, l := range lines[start:] {
		trimmed := strings.TrimSpace(l)
		// Depth is counted over CODE only: a comment line inside the block
		// that happens to contain the English word "if" or "fi" ("…a no-op
		// if the service isn't running") would otherwise unbalance the
		// counter and make this helper walk off the end of the file
		// (independent review of ut-docs#1040).
		if strings.HasPrefix(trimmed, "#") {
			body = append(body, l)
			continue
		}
		for _, f := range strings.Fields(trimmed) {
			switch strings.TrimSuffix(f, ";") {
			case "if":
				depth++
			case "fi":
				depth--
			}
		}
		body = append(body, l)
		if depth == 0 && len(body) > 1 {
			return codeLines(strings.Join(body, "\n"))
		}
	}
	t.Fatal("is_desktop_kiosk_overlay branch never closes")
	return nil
}

// TestDesktopOverlayBranchStagesAutostartAndSeedsDefaults asserts the
// overlay branch's actual actions: the autostart entry is written by
// unitill-desktop's own Go code (--install-autostart, run as the detected
// login user — never a bash heredoc duplicating the entry format), the two
// window settings are seeded through the repository layer (`unitill-pos
// provision-desktop-kiosk-defaults`, run as the pos service user against
// the service's own data dir — never raw SQL from bash), and the decision
// is echoed to the install log with the opt-out documented, mirroring the
// headless branch's UX.
func TestDesktopOverlayBranchStagesAutostartAndSeedsDefaults(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")
	block := overlayBranchBlock(t, post)

	// Login-user detection, same rule as unitill-kiosk-setup.sh --auto:
	// uid 1000, falling back to "pi".
	if !anyLineContains(block, "getent passwd 1000") {
		t.Error("overlay branch lost the uid-1000 login-user detection")
	}
	if !anyLineContains(block, ":-pi}") {
		t.Error(`overlay branch lost the "pi" login-user fallback`)
	}

	if !anyLineContains(block, "runuser", "/opt/unitill/bin/unitill-desktop", "--install-autostart") {
		t.Error("overlay branch no longer stages the XDG autostart entry via `unitill-desktop --install-autostart` as the login user")
	}
	if !anyLineContains(block, "runuser", "-u pos", "/opt/unitill/bin/unitill-pos", "provision-desktop-kiosk-defaults") {
		t.Error("overlay branch no longer seeds the window defaults via `unitill-pos provision-desktop-kiosk-defaults` as the pos user")
	}
	if !anyLineContains(block, "provision-desktop-kiosk-defaults", "--trigger=") {
		t.Error("overlay branch no longer records what triggered provisioning (--trigger= is gone)")
	}
	if !anyLineContains(block, "UT_DATA_DIR=/opt/unitill/data") {
		t.Error("overlay branch no longer pins UT_DATA_DIR to the service's data dir — the provisioner would seed a different database than unitill-pos.service reads")
	}

	// Independent review of ut-docs#1040: seeding the two settings into the
	// DB is NOT enough on its own. unitill-pos.service was already restarted
	// earlier in this same script, before those rows existed, and the server
	// caches them in memory (common.RuntimeState, seeded once by
	// pages.Init's LoadState). Left stale, that cache undoes the seeding
	// twice over — GET /api/window-mode serves launch_on_startup=false to
	// the autostarted shell, whose reconcileAutostart(false) deletes the
	// autostart entry this branch just staged; and the first-boot wizard's
	// common.SaveState rewrites the whole settings map from the stale cache,
	// putting window_mode back to "normal". So the branch must bounce the
	// service after a successful seed, and that bounce must come AFTER the
	// provisioning call, not before it.
	seedIdx, restartIdx := -1, -1
	for i, l := range block {
		if strings.Contains(l, "provision-desktop-kiosk-defaults") {
			seedIdx = i
		}
		if strings.Contains(l, "systemctl") && strings.Contains(l, "restart") && strings.Contains(l, "unitill-pos.service") {
			restartIdx = i
		}
	}
	if restartIdx < 0 {
		t.Error("overlay branch no longer restarts unitill-pos.service after seeding — the running server keeps serving its pre-seed cached window_mode/launch_on_startup, and the very first common.SaveState writes them back over the seeded values")
	} else if seedIdx < 0 || restartIdx < seedIdx {
		t.Errorf("overlay branch restarts unitill-pos.service at code line %d, before the seeding call at code line %d — the restart must follow the seed or it reloads the pre-seed values", restartIdx, seedIdx)
	}

	// The decision must reach the install log, with the opt-out documented —
	// mirroring the headless branch's "Raspberry Pi appliance detected" UX.
	if !anyLineContains(block, "echo", "desktop") {
		t.Error("overlay branch no longer echoes its decision to the install log")
	}
	if !anyLineContains(block, "no-desktop-kiosk-overlay") {
		t.Error("overlay branch no longer documents the opt-out marker in the install log")
	}

	// This branch must never reach for the headless machinery: no cage
	// setup, no first-boot unit, no display-manager changes.
	for _, l := range block {
		for _, forbidden := range []string{"unitill-kiosk-setup", "unitill-kiosk-firstboot", "unitill-kiosk.service", "display-manager.service"} {
			if strings.Contains(l, forbidden) {
				t.Errorf("overlay branch touches the headless-appliance machinery (%s): %q", forbidden, l)
			}
		}
	}
}

// TestHeadlessApplianceBranchUnchangedByOverlay pins the headless branch's
// observable contract after the ut-docs#1040 refactor (the shared
// is_fresh_install_pi_debian gate): same fresh-install-only gating, same
// staged first-boot unit, same opt-out, same UX line. Complements the
// still-passing assertions in kiosk_setup_test.go rather than replacing
// them.
func TestHeadlessApplianceBranchUnchangedByOverlay(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")

	if !strings.Contains(post, `if is_pi_appliance "${1:-}" "${2:-}"; then`) {
		t.Fatal("postinstall.sh: the is_pi_appliance gate line changed shape")
	}
	unit := heredocBlock(t, post, "/etc/systemd/system/unitill-kiosk-firstboot.service")
	if !anyLineContains(unit, "ExecStart=/usr/lib/unitill/unitill-kiosk-setup --auto") {
		t.Error("headless branch no longer stages unitill-kiosk-setup --auto")
	}
	if !anyLineContains(unit, "ConditionPathExists=!/etc/unitill/no-kiosk") {
		t.Error("headless branch lost its own /etc/unitill/no-kiosk opt-out")
	}
	if !strings.Contains(post, "Raspberry Pi appliance detected") {
		t.Error("headless branch lost its install-log UX line")
	}
}
