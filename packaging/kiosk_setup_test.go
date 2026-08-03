package packaging

// Regression guards for the Pi kiosk autostart fixes (ut-docs#6). Three root
// causes were diagnosed live on a field Pi5 (Debian 13/trixie); each has a
// specific line in the packaging scripts that must not regress, because the
// failure mode is a shop's till booting to a black console instead of the POS.
//
// The asserts deliberately operate on NON-COMMENT lines (and, for the unit
// files, only on the extracted heredoc block): the first version of this test
// used whole-file substring checks, and the independent review proved every
// fix could be reverted while a comment mentioning it kept the test green.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readScript(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// codeLines returns the script's lines with comments and blanks dropped, so
// an assert can't be satisfied by a mention in a comment.
func codeLines(s string) []string {
	var out []string
	for line := range strings.SplitSeq(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func anyLineContains(lines []string, subs ...string) bool {
	for _, l := range lines {
		ok := true
		for _, sub := range subs {
			if !strings.Contains(l, sub) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func shellFunction(t *testing.T, script, name string) string {
	t.Helper()
	start := strings.Index(script, name+"() {")
	if start < 0 {
		t.Fatalf("%s function not found", name)
	}
	end := strings.Index(script[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("%s function is not terminated", name)
	}
	return script[start : start+end+3]
}

func displayManagerGuardResult(t *testing.T, script, id, loadState string) bool {
	t.Helper()
	bin := t.TempDir()
	systemctl := filepath.Join(bin, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\nprintf '%s\\n%s\\n' \"$UT_SYSTEMD_ID\" \"$UT_SYSTEMD_LOAD_STATE\"\n"), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	cmd := exec.Command("sh", "-c", shellFunction(t, script, "has_real_display_manager")+"\nif has_real_display_manager; then exit 0; fi; exit 1")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "UT_SYSTEMD_ID="+id, "UT_SYSTEMD_LOAD_STATE="+loadState)
	return cmd.Run() == nil
}

// heredocBlock extracts the body of the `cat > <path> << EOF ... EOF` (or
// <<'EOF') block that writes the given file, comments stripped.
func heredocBlock(t *testing.T, script, path string) []string {
	t.Helper()
	lines := strings.Split(script, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "cat > "+path) && strings.Contains(l, "EOF") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("no heredoc writing %s found", path)
	}
	var body []string
	for _, l := range lines[start:] {
		if strings.TrimSpace(l) == "EOF" {
			return codeLines(strings.Join(body, "\n"))
		}
		body = append(body, l)
	}
	t.Fatalf("heredoc writing %s never terminated", path)
	return nil
}

func TestKioskServiceCarriesTheProvenTrixieFixes(t *testing.T) {
	setup := readScript(t, "packaging/linux/unitill-kiosk-setup.sh")
	unit := heredocBlock(t, setup, "/etc/systemd/system/unitill-kiosk.service")
	code := codeLines(setup)

	// (2) trixie boots with the active VT on tty7; without chvt 1 cage's
	// logind session never activates and it dies on "Timeout waiting session
	// to become active". The "+" (run as root) is load-bearing: the unit runs
	// as the kiosk user, who cannot switch VTs.
	if !anyLineContains(unit, "ExecStartPre=+/usr/bin/chvt 1") {
		t.Error("unitill-kiosk.service heredoc lost ExecStartPre=+/usr/bin/chvt 1 (as a real line, not a comment)")
	}
	// (3) PAMName=login means a logind session; libseat must be pinned to it
	// or a stray seatd wins and cage dies with "Broken pipe".
	if !anyLineContains(unit, "Environment=LIBSEAT_BACKEND=logind") {
		t.Error("unitill-kiosk.service heredoc lost Environment=LIBSEAT_BACKEND=logind (as a real line, not a comment)")
	}
	// seatd must NOT be installed by this script anymore — its presence is
	// exactly what triggered (3).
	if anyLineContains(code, "apt-get", "install", "seatd") {
		t.Error("unitill-kiosk-setup.sh installs seatd again")
	}
	// chvt comes from kbd; the install must come from a real apt-get line.
	if !anyLineContains(code, "apt-get", "install", "kbd") {
		t.Error("unitill-kiosk-setup.sh: no real apt-get install line provides kbd (chvt)")
	}
	// The first-boot path needs the non-interactive mode's arg parse, not a
	// comment mentioning it.
	if !anyLineContains(code, `"--auto"`, "AUTO=1") {
		t.Error("unitill-kiosk-setup.sh: the --auto arg parse is gone")
	}
	// A failed kiosk start must not be recorded as success: the marker write
	// exists, and the auto path gates on the service actually being active.
	if !anyLineContains(code, "touch /var/lib/unitill/kiosk-setup-done") {
		t.Error("unitill-kiosk-setup.sh: kiosk-setup-done marker write is gone")
	}
	if !anyLineContains(code, "is-active", "unitill-kiosk") {
		t.Error("unitill-kiosk-setup.sh: --auto no longer verifies the kiosk service became active")
	}
}

// TestPostinstallOwnsWholeInstallTreeForSelfUpdate guards ut-docs#151: a
// fresh .deb install must be self-updatable like every other install shape.
// selfupdate.Supported() requires BOTH /opt/unitill/bin (the binary's dir)
// and /opt/unitill (the server's cwd, per unitill-pos.service's
// WorkingDirectory) writable by the pos service user — narrower chowns of
// just data/items/assets leave both root-owned. The retired
// deploy/raspberry-pi/install.sh covered this with `chown -R pos:pos $DEST`;
// this asserts postinstall.sh carries the same fix, unconditionally (every
// invocation, not just fresh installs — a .deb upgrade re-extracts package
// files as root-owned, so it must reassert every time), for ANY .deb Linux
// install, not gated behind Pi detection.
// chownDepthAt scans script top-to-bottom (skipping comments, blank lines and
// heredoc bodies) tracking shell block nesting depth (if/for/while ... fi/
// done), and reports the depth at which the given exact, standalone code line
// first appears, plus whether it was found at all. Depth 0 means unindented
// AND not nested inside any conditional/loop — a raw substring/byte-offset
// check can't tell "inside an if with no leading whitespace" from genuinely
// unconditional, which is exactly how a prior version of this test passed
// against a fresh-install-only-gated chown (independent review, 2026-08-02).
func chownDepthAt(script, target string) (depth int, found bool) {
	inHeredoc := false
	for _, raw := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(raw)
		if inHeredoc {
			if trimmed == "EOF" {
				inHeredoc = false
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, "<<") {
			inHeredoc = true
			continue
		}
		if trimmed == target {
			return depth, true
		}
		for _, f := range strings.Fields(trimmed) {
			switch strings.TrimSuffix(f, ";") {
			case "if", "for", "while":
				depth++
			case "fi", "done":
				depth--
			}
		}
	}
	return 0, false
}

func TestPostinstallOwnsWholeInstallTreeForSelfUpdate(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")

	const chownLine = "chown -R pos:pos /opt/unitill"
	depth, found := chownDepthAt(post, chownLine)
	if !found {
		t.Fatal("postinstall.sh: no exact, standalone `chown -R pos:pos /opt/unitill` code line — a fresh .deb install stays root-owned at /opt/unitill/bin and /opt/unitill, so selfupdate.Supported() is false and Settings→Update has no in-app path (ut-docs#151). (A narrower /opt/unitill/data chown, or the line only inside a comment, does not satisfy this.)")
	}
	if depth != 0 {
		t.Errorf("postinstall.sh: `chown -R pos:pos /opt/unitill` is nested %d level(s) deep inside a conditional/loop — it must run unconditionally, at the top level, on every install AND upgrade, or a .deb upgrade re-roots the tree and self-update breaks again", depth)
	}

	if !strings.Contains(post, `if is_pi_appliance "${1:-}" "${2:-}"; then`) {
		t.Fatal("postinstall.sh: is_pi_appliance gate is gone — test needs updating")
	}
	chownRawIdx := strings.Index(post, "\n"+chownLine+"\n")
	piGateRawIdx := strings.Index(post, `if is_pi_appliance "${1:-}" "${2:-}"; then`)
	if chownRawIdx < 0 || chownRawIdx > piGateRawIdx {
		t.Error("postinstall.sh: chown -R pos:pos /opt/unitill does not precede the is_pi_appliance (fresh-install-only) gate — it must run before any conditional, on every invocation")
	}
}

func TestPostinstallStagesKioskFirstBootOnPiAppliancesOnly(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")
	code := codeLines(post)
	unit := heredocBlock(t, post, "/etc/systemd/system/unitill-kiosk-firstboot.service")

	// (1) the original field bug: a fresh .deb never enabled the kiosk at
	// all. The postinstall must detect a Pi and stage the first-boot setup.
	if !anyLineContains(code, `"Raspberry Pi"`, "/proc/device-tree/model") {
		t.Error("postinstall.sh: Raspberry Pi detection is gone")
	}
	if !anyLineContains(unit, "ExecStart=/usr/lib/unitill/unitill-kiosk-setup --auto") {
		t.Error("postinstall.sh: first-boot unit no longer runs unitill-kiosk-setup --auto")
	}
	// The three review-mandated gates (2026-07-31): fresh installs only,
	// Debian-family only, and never on a box with a display manager.
	if !anyLineContains(code, `[ -z "$2" ]`) {
		t.Error("postinstall.sh: upgrade guard ($2 empty = fresh install) is gone — upgrades would kiosk-ify existing field Pis")
	}
	if !anyLineContains(code, "ID=(debian|raspbian)") {
		t.Error("postinstall.sh: Debian-family os-release gate is gone — Ubuntu-on-Pi would get a snap chromium under cage")
	}
	if !anyLineContains(code, "systemctl show", "--property=Id,LoadState", "display-manager.service") {
		t.Error("postinstall.sh: resolved display-manager gate is gone — a desktop Pi would silently lose its desktop")
	}
	// Retry semantics: a failed first boot (offline shop) must retry rather
	// than dead-ending at a console.
	if !anyLineContains(unit, "Restart=on-failure") {
		t.Error("first-boot unit lost Restart=on-failure — an offline first boot would never retry within the boot")
	}
	// Orphan guard: the unit must not fire against a removed package.
	if !anyLineContains(unit, "ConditionPathExists=/usr/lib/unitill/unitill-kiosk-setup") {
		t.Error("first-boot unit lost its binary-exists condition")
	}
	// Dev boxes need the documented opt-out.
	if !anyLineContains(unit, "ConditionPathExists=!/etc/unitill/no-kiosk") {
		t.Error("first-boot unit lost the /etc/unitill/no-kiosk opt-out condition")
	}
	// The kiosk setup runs apt and therefore must NOT be invoked directly
	// from inside the dpkg transaction — only via the staged unit.
	for _, l := range code {
		if strings.Contains(l, "unitill-kiosk-setup") &&
			!strings.Contains(l, ".service") && !strings.Contains(l, "ExecStart") &&
			!strings.Contains(l, "ConditionPathExists") {
			t.Errorf("postinstall.sh runs unitill-kiosk-setup inside the dpkg transaction: %q", l)
		}
	}
}

func TestKioskDisplayManagerGuardsResolveAliases(t *testing.T) {
	// On the field Pi, display-manager.service was a symlink alias to a
	// target. `systemctl is-enabled` returned success for that alias even
	// though no display-manager service existed, which skipped automatic
	// kiosk setup and made the manual helper fail while stopping it.
	for _, rel := range []string{
		"packaging/scripts/postinstall.sh",
		"packaging/linux/unitill-kiosk-setup.sh",
	} {
		code := codeLines(readScript(t, rel))
		if !anyLineContains(code, "systemctl show", "--property=Id,LoadState", "display-manager.service") {
			t.Errorf("%s: display-manager guard must resolve the canonical systemd unit ID and load state before treating an alias as a desktop manager", rel)
		}
		if !anyLineContains(code, `"$display_manager_load_state"`, "loaded") {
			t.Errorf("%s: display-manager guard must reject a non-loaded service name", rel)
		}
		if !anyLineContains(code, "*.service") {
			t.Errorf("%s: display-manager guard must accept only a resolved service unit, not a target alias", rel)
		}
		if displayManagerGuardResult(t, readScript(t, rel), "graphical.target", "loaded") {
			t.Errorf("%s: a display-manager alias to graphical.target must not be treated as a desktop manager", rel)
		}
		if !displayManagerGuardResult(t, readScript(t, rel), "lightdm.service", "loaded") {
			t.Errorf("%s: a loaded lightdm.service must still be treated as a desktop manager", rel)
		}
		if displayManagerGuardResult(t, readScript(t, rel), "display-manager.service", "not-found") {
			t.Errorf("%s: a non-loaded display-manager service name must not be treated as a desktop manager", rel)
		}
	}
}

func TestRemovalScriptsCleanUpKioskUnits(t *testing.T) {
	pre := codeLines(readScript(t, "packaging/scripts/preremove.sh"))
	post := codeLines(readScript(t, "packaging/scripts/postremove.sh"))

	if !anyLineContains(pre, "disable --now unitill-kiosk.service") {
		t.Error("preremove.sh no longer stops the kiosk — cage would restart-loop against a removed binary")
	}
	if !anyLineContains(pre, "disable --now unitill-kiosk-firstboot.service") {
		t.Error("preremove.sh no longer stops the first-boot unit")
	}
	if !anyLineContains(post, "rm -f /etc/systemd/system/unitill-kiosk.service") {
		t.Error("postremove.sh no longer removes the non-package-owned kiosk units")
	}
	if !anyLineContains(post, "set-default multi-user.target") {
		t.Error("postremove.sh no longer hands the console back to getty on removal")
	}
}

func TestPackagingShellScriptsParse(t *testing.T) {
	// postinstall/preremove/postremove run under /bin/sh on the target box;
	// the kiosk setup declares bash. A bashism creeping into the sh scripts
	// fails at install time on a shop's device — catch it in CI instead.
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	for _, rel := range []string{
		"packaging/scripts/postinstall.sh",
		"packaging/scripts/preremove.sh",
		"packaging/scripts/postremove.sh",
	} {
		out, err := exec.Command(shPath, "-n", filepath.Join("..", rel)).CombinedOutput()
		if err != nil {
			t.Errorf("%s does not parse under sh -n: %v\n%s", rel, err, out)
		}
	}
	if bashPath, err := exec.LookPath("bash"); err == nil {
		out, err := exec.Command(bashPath, "-n", filepath.Join("..", "packaging/linux/unitill-kiosk-setup.sh")).CombinedOutput()
		if err != nil {
			t.Errorf("unitill-kiosk-setup.sh does not parse under bash -n: %v\n%s", err, out)
		}
	}
}

func TestStaleX11KioskPathStaysRetired(t *testing.T) {
	// The old deploy/raspberry-pi X11 flow (DISPLAY=:0, desktop autologin)
	// contradicted the shipped Wayland/cage path and misled setup on real
	// devices — it was retired with ut-docs#6. Keep it gone.
	if _, err := os.Stat(filepath.Join("..", "deploy", "raspberry-pi")); !os.IsNotExist(err) {
		t.Error("deploy/raspberry-pi is back — the retired X11 kiosk path must not return")
	}
}

// TestKioskHelpersStayOffPosWritableTree guards ut-docs#255: ut-docs#151 made
// the whole /opt/unitill tree pos-writable (`chown -R pos:pos /opt/unitill`
// in postinstall.sh, required for selfupdate.Supported()). unitill-kiosk-setup
// is root-executed (unitill-kiosk-firstboot.service has no User=, and the
// manual `sudo unitill-kiosk-setup` flow is root too) — if its install/
// reference path is anywhere under /opt/unitill, the pos service user (runs
// the network-facing HTTP server + in-process third-party WASM plugins) can
// plant a script root will execute. unitill-kiosk-launch runs as the kiosk
// user, not root (only unitill-kiosk.service's ExecStartPre=+chvt is root),
// but the same pos-writable exposure still lets pos hand the kiosk session
// an arbitrary script, so it moves too. Asserted as a property
// (not-under-/opt/unitill), not just equality to one literal new path, so a
// future edit landing either script back under /opt/unitill in a different
// exact string still fails this.
func TestKioskHelpersStayOffPosWritableTree(t *testing.T) {
	assertNotUnderOptUnitill := func(t *testing.T, label, path string) {
		t.Helper()
		if path == "" {
			t.Errorf("%s: path not found", label)
			return
		}
		if strings.Contains(path, "/opt/unitill") {
			t.Errorf("%s resolves under /opt/unitill (%s) — that tree is pos-writable (postinstall.sh's chown -R pos:pos /opt/unitill, ut-docs#151), so a root-executed helper must never live there (ut-docs#255)", label, path)
		}
	}

	// nfpm package contents (.goreleaser.yaml): where the .deb installs the
	// two scripts.
	goreleaser, err := os.ReadFile(filepath.Join("..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	setupDst := nfpmDst(string(goreleaser), "unitill-kiosk-setup.sh")
	launchDst := nfpmDst(string(goreleaser), "unitill-kiosk-launch.sh")
	assertNotUnderOptUnitill(t, ".goreleaser.yaml nfpm dst for unitill-kiosk-setup", setupDst)
	assertNotUnderOptUnitill(t, ".goreleaser.yaml nfpm dst for unitill-kiosk-launch.sh", launchDst)
	// The two scripts are read from the same directory at runtime
	// (unitill-kiosk-setup.sh's `install -D "$(dirname "$0")/unitill-kiosk-launch.sh" ...`)
	// — nfpm shipping them to different directories would make setup fail to
	// find its sibling (independent review finding, ut-docs#255).
	if setupDst != "" && launchDst != "" && filepath.Dir(setupDst) != filepath.Dir(launchDst) {
		t.Errorf("nfpm ships unitill-kiosk-setup to %s but unitill-kiosk-launch.sh to %s — unitill-kiosk-setup.sh looks for its sibling launch script via $(dirname \"$0\"), so they must share a directory", setupDst, launchDst)
	}

	// postinstall.sh's first-boot unit: what it ExecStarts/ConditionPathExists as root.
	post := readScript(t, "packaging/scripts/postinstall.sh")
	unit := heredocBlock(t, post, "/etc/systemd/system/unitill-kiosk-firstboot.service")
	execStart := lineValue(unit, "ExecStart=")
	assertNotUnderOptUnitill(t, "unitill-kiosk-firstboot.service ExecStart", execStart)
	condPath := lineContaining(unit, "ConditionPathExists=", "unitill-kiosk-setup")
	assertNotUnderOptUnitill(t, "unitill-kiosk-firstboot.service ConditionPathExists(unitill-kiosk-setup)", condPath)

	// unitill-kiosk-setup.sh: where it self-installs the launch script, and
	// what the kiosk.service unit it writes ExecStarts as the kiosk user.
	setup := readScript(t, "packaging/linux/unitill-kiosk-setup.sh")
	setupCode := codeLines(setup)
	installDst := ""
	for _, l := range setupCode {
		if strings.HasPrefix(l, "install -D") && strings.Contains(l, "unitill-kiosk-launch") {
			fields := strings.Fields(l)
			installDst = fields[len(fields)-1]
		}
	}
	assertNotUnderOptUnitill(t, "unitill-kiosk-setup.sh install -D destination for unitill-kiosk-launch", installDst)

	kioskUnit := heredocBlock(t, setup, "/etc/systemd/system/unitill-kiosk.service")
	execStartCmd := lineValue(kioskUnit, "ExecStart=")
	assertNotUnderOptUnitill(t, "unitill-kiosk.service ExecStart", execStartCmd)
	// The unit ExecStarts the exact path unitill-kiosk-setup.sh just
	// installed the launch script to — a mismatch here means cage starts
	// against a path nothing was ever copied to (independent review
	// finding, ut-docs#255: verified live with a deliberately mismatched
	// pair, and the individual not-under-/opt/unitill checks above stayed
	// green against it).
	execStartFields := strings.Fields(execStartCmd)
	execStartTarget := ""
	if len(execStartFields) > 0 {
		execStartTarget = execStartFields[len(execStartFields)-1]
	}
	if installDst != "" && execStartTarget != "" && installDst != execStartTarget {
		t.Errorf("unitill-kiosk-setup.sh installs the launch script to %s but unitill-kiosk.service ExecStarts %s", installDst, execStartTarget)
	}
}

// TestPostinstallMigratesStaleKioskUnitsFromBeforeUsrLibMove guards the
// upgrade path for ut-docs#255. unitill-kiosk-firstboot.service and
// unitill-kiosk.service are NOT dpkg-managed (both written by heredoc at
// install/setup time, not shipped as package `contents:`), so moving the
// scripts' *package* install path alone (the fix above) does nothing for a
// box that installed/set up its kiosk before this package version — its
// on-disk unit files still say /opt/unitill/bin, and dpkg upgrading the
// package to the new nfpm dst does not touch them. Concretely, on such a
// box: the pos->root path this ticket exists to close still exists (the
// stale firstboot unit's ConditionPathExists/ExecStart still point at
// /opt/unitill/bin/unitill-kiosk-setup, which chown -R pos:pos /opt/unitill
// still makes pos-writable every invocation), and a box whose kiosk was
// already set up loses its kiosk launcher entirely once nfpm stops shipping
// anything at the old /opt/unitill/bin/unitill-kiosk-launch.sh path
// (independent review finding, ut-docs#255). This asserts postinstall.sh
// migrates both stale units in place, unconditionally (every invocation,
// not gated behind the fresh-install-only is_pi_appliance check) — same
// "runs on every invocation, not just fresh installs" property the existing
// chown assertion above already requires of itself.
func TestPostinstallMigratesStaleKioskUnitsFromBeforeUsrLibMove(t *testing.T) {
	post := readScript(t, "packaging/scripts/postinstall.sh")
	code := codeLines(post)

	if !anyLineContains(code, "unitill-kiosk-firstboot.service", "sed", "/opt/unitill/bin/unitill-kiosk-setup", "/usr/lib/unitill/unitill-kiosk-setup") {
		t.Error("postinstall.sh: no in-place rewrite of a stale unitill-kiosk-firstboot.service's /opt/unitill/bin/unitill-kiosk-setup reference to /usr/lib/unitill/unitill-kiosk-setup — a box that installed before ut-docs#255 keeps the pos->root path open after upgrading")
	}
	if !anyLineContains(code, "unitill-kiosk.service", "sed", "/opt/unitill/bin/unitill-kiosk-launch", "/usr/lib/unitill/unitill-kiosk-launch") {
		t.Error("postinstall.sh: no in-place rewrite of a stale unitill-kiosk.service's /opt/unitill/bin/unitill-kiosk-launch reference to /usr/lib/unitill/unitill-kiosk-launch")
	}
	if !anyLineContains(code, "cp", "/opt/unitill/bin/unitill-kiosk-launch", "/usr/lib/unitill/unitill-kiosk-launch") {
		t.Error("postinstall.sh: repoints unitill-kiosk.service at /usr/lib/unitill/unitill-kiosk-launch without ever copying the already-installed launch script there — an already-kiosked Pi's launcher would reference a path nothing put a file at")
	}

	// Must run on every invocation, not just fresh installs — same property
	// the chown -R pos:pos /opt/unitill assertion above already enforces on
	// itself, and for the same reason: an upgrade must self-heal.
	migrationRawIdx := strings.Index(post, "/opt/unitill/bin/unitill-kiosk-setup")
	// The first mention of this literal in the (fixed) script is inside a
	// comment above the chown line explaining why the split exists; skip
	// past any comment lines to find the first CODE line that mentions it.
	migrationCodeIdx := -1
	for _, l := range code {
		if strings.Contains(l, "/opt/unitill/bin/unitill-kiosk-setup") {
			migrationCodeIdx = strings.Index(post, l)
			break
		}
	}
	if migrationRawIdx < 0 || migrationCodeIdx < 0 {
		t.Fatal("postinstall.sh: no migration code line references /opt/unitill/bin/unitill-kiosk-setup at all")
	}
	piGateRawIdx := strings.Index(post, `if is_pi_appliance "${1:-}" "${2:-}"; then`)
	if piGateRawIdx < 0 {
		t.Fatal("postinstall.sh: is_pi_appliance gate is gone — test needs updating")
	}
	if migrationCodeIdx > piGateRawIdx {
		t.Error("postinstall.sh: the stale-unit migration is positioned after the is_pi_appliance (fresh-install-only) gate — it must run unconditionally, before any conditional, on every invocation, or upgrades on non-Pi-appliance boxes (any plain Debian .deb install) never get migrated")
	}
}

// nfpmDst returns the `dst:` value of the nfpm (`.deb`) contents entry whose
// `src:` ends with the given filename. Tolerates the `- src: ...` list-item
// form. Scoped to start after the `nfpms:` key — the same two scripts also
// appear (under different, irrelevant dst paths) in the plain tar.gz
// `archives:` section earlier in the file, which has no root/pos split and
// must not be matched here.
func nfpmDst(yaml, srcSuffix string) string {
	if idx := strings.Index(yaml, "\nnfpms:"); idx >= 0 {
		yaml = yaml[idx:]
	}
	lines := strings.Split(yaml, "\n")
	for i, l := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "-"))
		trimmed = strings.TrimSpace(trimmed)
		if strings.HasPrefix(trimmed, "src:") && strings.HasSuffix(trimmed, srcSuffix) {
			for _, next := range lines[i+1:] {
				nt := strings.TrimSpace(next)
				if strings.HasPrefix(nt, "dst:") {
					return strings.TrimSpace(strings.TrimPrefix(nt, "dst:"))
				}
				if strings.HasPrefix(nt, "src:") || strings.HasPrefix(strings.TrimPrefix(nt, "- "), "src:") {
					break
				}
			}
		}
	}
	return ""
}

// lineValue returns the value after the first code line starting with
// prefix (e.g. "ExecStart=/usr/lib/unitill/x" for prefix "ExecStart=").
func lineValue(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimPrefix(l, prefix)
		}
	}
	return ""
}

// lineContaining returns the value after prefix on the first code line that
// starts with prefix AND contains sub — for picking one specific
// ConditionPathExists= line out of several on the same unit.
func lineContaining(lines []string, prefix, sub string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) && strings.Contains(l, sub) {
			return strings.TrimPrefix(l, prefix)
		}
	}
	return ""
}
