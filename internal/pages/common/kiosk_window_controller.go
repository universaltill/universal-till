package common

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// systemctlKioskTimeout bounds one systemctl call. `systemctl start` can
// block on the unit's own job (unitill-kiosk-setup.sh's own comment warns a
// blocking start can stall on the Conflicts=getty@tty1 stop job) — without a
// ceiling, a stuck job would pin the Settings HTTP handler goroutine
// indefinitely instead of surfacing the clear error ut-docs#883 asks for.
const systemctlKioskTimeout = 30 * time.Second

// KioskSystemdWindowController is the WindowController for the Raspberry Pi
// headless kiosk appliance (ut-docs#883): it drives the real
// unitill-kiosk.service via `sudo systemctl`, using the narrowly-scoped
// NOPASSWD sudoers grant `unitill-kiosk-setup.sh` installs for exactly the
// four enable/disable/start/stop calls below — no other unit, no wildcard
// (that script's own comment explains why: this is a distinct security
// surface from the desktop-shell cards, worth its own scoped grant). Set as
// Deps.WindowCtl by pages.Init only on the Pi kiosk path (UT_KIOSK=1 on
// Linux) — never for the desktop shell (#609/#610/#611) or a plain browser
// session, both of which keep NoopWindowController/their own controller.
type KioskSystemdWindowController struct {
	// run executes one systemctl subcommand against unitill-kiosk.service.
	// Overridable in tests with a fake command runner; nil falls back to the
	// real `sudo systemctl <verb> unitill-kiosk.service`.
	run func(verb string) error
}

// NewKioskSystemdWindowController returns a controller that shells out to
// the real `sudo systemctl`.
func NewKioskSystemdWindowController() KioskSystemdWindowController {
	return KioskSystemdWindowController{run: runSystemctlKiosk}
}

// systemctlKioskArgs builds the sudo argv for one verb — split out from
// runSystemctlKiosk so the exact flags (in particular `-n`, see
// systemctlKioskTimeout's sibling reasoning above) are unit-testable without
// actually invoking sudo.
func systemctlKioskArgs(verb string) []string {
	// -n (--non-interactive): without it, a missing sudoers grant makes sudo
	// attempt an interactive password prompt instead of failing immediately.
	return []string{"-n", "systemctl", verb, "unitill-kiosk.service"}
}

func runSystemctlKiosk(verb string) error {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlKioskTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sudo", systemctlKioskArgs(verb)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s unitill-kiosk.service: %w: %s", verb, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ExitToOS is a deliberate no-op on the Pi headless kiosk path — there is no
// OS desktop to exit to (cage occupies the whole console); the operator's
// way out on this platform is the window-mode toggle itself (ApplyMode),
// not exit-to-os.
func (KioskSystemdWindowController) ExitToOS() error { return nil }

// ApplyMode enables+starts unitill-kiosk.service for "kiosk" mode, and
// disables+stops it for any other mode — replacing the file-touch-only
// /etc/unitill/no-kiosk opt-out (still honoured as a documented manual
// fallback, not removed) with a real toggle from Settings. Errors surface
// clearly rather than a silent no-op: a Pi that installed before ut-docs#883
// (upgraded, not re-run through unitill-kiosk-setup.sh) won't have the
// sudoers grant yet, and that failure must reach the operator, not vanish.
func (c KioskSystemdWindowController) ApplyMode(mode string) error {
	run := c.run
	if run == nil {
		run = runSystemctlKiosk
	}
	verbs := []string{"disable", "stop"}
	if mode == "kiosk" {
		verbs = []string{"enable", "start"}
	}
	for _, verb := range verbs {
		if err := run(verb); err != nil {
			return err
		}
	}
	return nil
}
