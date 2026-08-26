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
	"strconv"
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

// autostartStagingSnippet extracts the RAW (comments kept — this is
// executed, not just grepped) source from the `AUTOSTART_STAGED=false`
// line through the matching `fi` that closes the `--install-autostart`
// if/else — stopping well before the separate `provision-desktop-kiosk-
// defaults` call and its own systemctl try-restart, so a test running this
// snippet needs no fake for those at all.
func autostartStagingSnippet(t *testing.T, script string) string {
	t.Helper()
	lines := strings.Split(script, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "AUTOSTART_STAGED=false") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("postinstall.sh: AUTOSTART_STAGED=false not found")
	}
	depth := 0
	seenIf := false
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "#") {
			for _, f := range strings.Fields(trimmed) {
				switch strings.TrimSuffix(f, ";") {
				case "if":
					depth++
					seenIf = true
				case "fi":
					depth--
				}
			}
		}
		if seenIf && depth == 0 {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatal("postinstall.sh: --install-autostart if/else block never closes")
	return ""
}

// runAutostartStaging executes autostartStagingSnippet against a fake
// `runuser` that simulates `unitill-desktop --install-autostart`: exits
// with runuserExit, and — independent of that exit code, exactly like the
// real ut-docs#1094 failure (exit 0, wrote nothing) — writes the target
// file only when writeFile is true. OVERLAY_USER/OVERLAY_HOME are pinned
// via a fake `getent`+`id` to a real temp dir, so the snippet's own
// `$OVERLAY_HOME/.config/autostart/unitill.desktop` check operates on a
// real, disposable filesystem path. Returns the snippet's own
// AUTOSTART_STAGED result and everything written to stderr.
func runAutostartStaging(t *testing.T, script string, runuserExit int, writeFile bool) (staged bool, stderr string) {
	t.Helper()
	snippet := autostartStagingSnippet(t, script)
	bin := t.TempDir()
	home := t.TempDir()

	writeFake := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	writeFake("id", "exit 0\n") // OVERLAY_USER always resolves
	writeFake("getent", `printf 'ut-overlay-user:x:1000:1000::`+home+`:/bin/sh\n'`+"\n")
	// The fake `unitill-desktop --install-autostart`, reached via runuser:
	// real args are `-u OVERLAY_USER -- env -u XDG_CONFIG_HOME
	// /opt/unitill/bin/unitill-desktop --install-autostart` — this fake
	// ignores all of them (it doesn't need to distinguish; the script under
	// test is what chooses the args) and just does what the flags below say.
	installAutostartBody := "exit " + strconv.Itoa(runuserExit) + "\n"
	if writeFile {
		installAutostartBody = "mkdir -p " + home + "/.config/autostart && touch " + home + "/.config/autostart/unitill.desktop\n" + installAutostartBody
	}
	writeFake("runuser", installAutostartBody)

	harness := snippet + "\nif [ \"$AUTOSTART_STAGED\" = true ]; then echo STAGED=true; else echo STAGED=false; fi\n"
	cmd := exec.Command("sh", "-c", harness)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "OVERLAY_USER=ut-overlay-user")
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("run autostart staging snippet: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}
	return strings.Contains(out.String(), "STAGED=true"), errOut.String()
}

// TestDesktopOverlayBranchAutostartHomeAndConfigHomeAreExplicit pins the
// ut-docs#1094 finding, corrected after independent review: `runuser -u
// USER --` DOES reset $HOME/$USER/etc to the target user's own by default
// (util-linux's `-m`/`--preserve-environment` is the opt-OUT — an earlier
// draft of this fix wrongly assumed the opposite and shipped a no-op
// `env HOME=` override). The real remaining gap is $XDG_CONFIG_HOME, which
// runuser does NOT reset and which Go's os.UserConfigDir() checks BEFORE
// falling back to $HOME/.config — a stray inherited value would still
// redirect the autostart entry to the wrong place.
func TestDesktopOverlayBranchAutostartHomeAndConfigHomeAreExplicit(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")
	snippet := codeLines(autostartStagingSnippet(t, post))

	if !anyLineContains(snippet, "env -u XDG_CONFIG_HOME", "--install-autostart") {
		t.Error("overlay branch's --install-autostart call no longer clears an inherited XDG_CONFIG_HOME — os.UserConfigDir() checks it before $HOME, so a stray value would still redirect the autostart entry")
	}
	// A reintroduced `env HOME=...` override is a real regression to catch,
	// not a harmless leftover: independent review found it can only ever
	// differ from what runuser already sets correctly on its own — SHOULD-
	// FIX 5, an empty getent home field would force HOME="" and break a
	// case that worked before the override existed.
	if anyLineContains(snippet, "env HOME=") {
		t.Error("overlay branch passes an explicit HOME= override again — runuser -u already resets HOME correctly on its own (confirmed by measurement); overriding it can only make an edge case (an empty getent home field) worse, never better")
	}
}

// TestDesktopOverlayBranchAutostartStagesOnlyWhenTheFileActuallyExists
// actually RUNS the extracted staging snippet (same convention this file's
// header sets: execute against fakes, not substring-check comments) against
// a fake `unitill-desktop --install-autostart` that can independently
// control its exit code and whether it writes the file — proving the
// branch trusts the filesystem, not the exit code, for the exact
// ut-docs#1094 failure shape (exit 0, wrote nothing).
func TestDesktopOverlayBranchAutostartStagesOnlyWhenTheFileActuallyExists(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")

	t.Run("exit 0 and the file exists: staged", func(t *testing.T) {
		staged, _ := runAutostartStaging(t, post, 0, true)
		if !staged {
			t.Error("AUTOSTART_STAGED = false, want true — install-autostart succeeded and the file is really there")
		}
	})

	t.Run("exit 0 but the file was never written (the real ut-docs#1094 failure): NOT staged, warned loudly", func(t *testing.T) {
		staged, stderr := runAutostartStaging(t, post, 0, false)
		if staged {
			t.Error("AUTOSTART_STAGED = true, want false — install-autostart reported success but wrote nothing; trusting the exit code alone is the exact bug this hardening fixes")
		}
		if !strings.Contains(stderr, "reported success, but") || !strings.Contains(stderr, "does not exist") {
			t.Errorf("stderr = %q, want a warning naming the missing path (\"reported success, but ... does not exist\")", stderr)
		}
	})

	t.Run("nonzero exit: NOT staged, warned", func(t *testing.T) {
		staged, stderr := runAutostartStaging(t, post, 1, false)
		if staged {
			t.Error("AUTOSTART_STAGED = true, want false — install-autostart itself reported failure")
		}
		if !strings.Contains(stderr, "could not stage") {
			t.Errorf("stderr = %q, want the could-not-stage warning", stderr)
		}
	})
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
