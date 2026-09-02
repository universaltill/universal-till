// Package pihealth does a best-effort local check of the Raspberry Pi's
// reported power-supply health, so the till can show a persistent
// "underpowered" chip in its status bar (ut-docs#1232). The incident that
// prompted this: an inadequate PSU restricted USB peripheral current on a
// Pi 5 (the till's own touch panel is USB) and a shop owner would never see
// it — the only warning was a boot-log/desktop message nobody watches on a
// kiosk. Runs entirely locally (no network, no DB), every failure is
// silent, and it is a complete no-op on any non-Raspberry-Pi platform —
// same "best-effort, never take the till down" shape as internal/updates.
package pihealth

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Status is the last known power-supply health.
type Status struct {
	Underpowered bool
}

var state atomic.Value // Status

// Current returns the last checked status (zero value before the first
// check, and permanently zero on a non-Pi platform — Start never even
// launches the checker there, and CheckNow's own isPi() guard keeps a
// direct call harmless too).
func Current() Status {
	if s, ok := state.Load().(Status); ok {
		return s
	}
	return Status{}
}

// CheckNow performs one synchronous check and returns the freshest status.
func CheckNow(ctx context.Context) Status {
	checkOnce(ctx)
	return Current()
}

// Start launches the background checker: once ~15s after boot, then every 5
// minutes for as long as ctx stays alive. A no-op on any platform that
// isn't a Raspberry Pi — isPi() is checked up front so a thin client / mac
// dev machine / Windows box never spawns the goroutine at all. wg is marked
// Done once the checker goroutine has fully exited (ctx cancelled) —
// callers use it to wait out shutdown instead of returning while the
// goroutine still runs (mirrors internal/updates.Start's shape exactly).
func Start(ctx context.Context, wg *sync.WaitGroup) {
	if !isPi() {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-time.After(15 * time.Second):
		case <-ctx.Done():
			return
		}
		checkOnce(ctx)
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				checkOnce(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// checkOnce is the composed decision. Once latched true on a Pi, it stays
// true for the rest of the process's life — it deliberately never flips
// back to false on its own (review finding, ut-docs#1232): a transient
// vcgencmd/device-tree read failure, or the negotiated-current node simply
// not being re-readable for a moment, must never make an already-shown
// warning silently vanish while the underlying PSU is still wrong. Clearing
// it for real needs a reboot on a proper supply (which starts this process
// fresh with a clean Status{}), matching what the status-bar chip's own
// copy and the help topic both already promise.
func checkOnce(ctx context.Context) {
	if !isPi() {
		state.Store(Status{})
		return
	}
	if Current().Underpowered {
		return
	}
	underpowered := false
	if out, err := runVcgencmd(ctx); err == nil && throttledIndicatesUnderpower(out) {
		underpowered = true
	}
	if !underpowered && negotiatedCurrentBelowThreshold() {
		underpowered = true
	}
	if underpowered {
		state.Store(Status{Underpowered: true})
	}
}

// --- Raspberry Pi detection ---

// deviceTreeModelPath is a var purely as a test seam (mirrors
// internal/updates' releasesURL — a test points this at a temp file instead
// of shelling out or touching the real host device tree).
var deviceTreeModelPath = "/proc/device-tree/model"

// isPi reports whether this host is a Raspberry Pi, via the standard
// Raspberry Pi OS/Debian device-tree model file: present (null-terminated,
// e.g. "Raspberry Pi 5 Model B Rev 1.0\x00") on every real Pi, absent
// everywhere else — including inside a container without the host device
// tree bind-mounted, which correctly reads as "not a Pi" rather than
// guessing or erroring.
func isPi() bool {
	b, err := os.ReadFile(deviceTreeModelPath)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimRight(string(b), "\x00\n"), "Raspberry Pi")
}

// --- vcgencmd get_throttled ---

// runVcgencmd is a var purely as a test seam — override it in a test
// instead of shelling out for real (same pattern as deviceTreeModelPath
// above / internal/updates' releasesURL).
var runVcgencmd = func(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "vcgencmd", "get_throttled").Output()
	return string(out), err
}

// throttledIndicatesUnderpower parses vcgencmd get_throttled's
// "throttled=0x50000"-shaped output (documented Raspberry Pi firmware
// flags). Bit 0 = under-voltage detected right now; bit 16 = under-voltage
// has occurred since boot — that second bit is sticky (stays set once
// tripped, even after the moment passes). Bits for frequency capping/
// thermal throttling (1,2,3,17,18,19) are a different problem (cooling, not
// PSU) and deliberately not treated as "underpowered" here. This signal
// needs the rail to have actually sagged under load — it's a real-time ADC
// reading, not a negotiation outcome — so it complements, rather than
// duplicates, negotiatedCurrentBelowThreshold below, which can catch the
// problem before any load ever trips this.
func throttledIndicatesUnderpower(out string) bool {
	_, hex, ok := strings.Cut(strings.TrimSpace(out), "=")
	if !ok {
		return false
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "0x"), 16, 64)
	if err != nil {
		return false
	}
	const underVoltageNow, underVoltageSinceBoot = 0x1, 0x10000
	return v&(underVoltageNow|underVoltageSinceBoot) != 0
}

// --- negotiated PSU max-current (device tree, USB-PD boards) ---

// maxCurrentPath is a var purely as a test seam, same reasoning as
// deviceTreeModelPath above.
var maxCurrentPath = "/proc/device-tree/chosen/power/max_current"

// minAdequateCurrentMA is the current (in mA) a fully-rated 5V/5A supply
// negotiates. A Pi 5 negotiates its USB-PSU budget over USB-C PD at boot,
// independent of load, and the firmware publishes the outcome here as a
// big-endian uint32 in milliamps (ut-docs#1232 review finding: this is the
// actual source of the desktop/boot "not capable of supplying 5A" warning
// quoted in that card's own finding — that message is generated by
// userspace tooling reading this exact node, not logged to the kernel ring
// buffer, and the wording varies across tools/locales, so matching it as
// text is both wrong and unreliable; reading the node directly is not).
// A negotiated value below this means the firmware has already decided the
// attached PSU can't do 5A and capped USB peripheral current accordingly —
// catching the problem immediately at boot, before any load could trip
// throttledIndicatesUnderpower's ADC-measured bits above. Pi 4 and earlier
// have no USB-PD negotiation and no such node — a read error there
// correctly reports no signal rather than a false problem.
const minAdequateCurrentMA = 5000

func negotiatedCurrentBelowThreshold() bool {
	b, err := os.ReadFile(maxCurrentPath)
	if err != nil || len(b) < 4 {
		return false
	}
	ma := binary.BigEndian.Uint32(b[:4])
	return ma > 0 && ma < minAdequateCurrentMA
}
