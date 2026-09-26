// Package fxlevel is the per-till visual effects level (ADR-0119,
// ut-docs#2859): the setting's values and keys, and the host detection that
// resolves "auto" from the device's own hardware — CPU cores, total RAM
// where the platform exposes it, and whether it is a Raspberry Pi.
//
// Detection is local only (a couple of small file reads), never touches the
// network and never blocks startup. The in-page frame-time probe and the
// shell-engine signal of ADR-0119 §4 are a follow-up card; until then host
// detection alone decides, and it never yields Balanced.
package fxlevel

import (
	"bufio"
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/pihealth"
)

// The setting's values. Auto is only ever a setting; the page renders the
// level it resolved to (ADR-0119 §2).
const (
	Auto     = "auto"
	Full     = "full"
	Balanced = "balanced"
	Light    = "light"
)

// Settings keys, all per-till under the "display." prefix
// (data.PerTillSettingPrefixes): the operator's choice, and the last host
// detection with its reason and the hardware fingerprint it was made on.
const (
	SettingsPrefix = "display.effects_"
	KeyLevel       = SettingsPrefix + "level"
	KeyDetected    = SettingsPrefix + "detected"
	KeyReason      = SettingsPrefix + "reason"
	KeyFingerprint = SettingsPrefix + "fingerprint"
)

// ValidSetting reports whether v is a value display.effects_level may hold.
func ValidSetting(v string) bool {
	return v == Auto || ValidResolved(v)
}

// ValidResolved reports whether v is a level a page can render.
func ValidResolved(v string) bool {
	switch v {
	case Full, Balanced, Light:
		return true
	}
	return false
}

// Thresholds for the host rule (ADR-0119 §4, starting guesses until the
// device-measurement card calibrates them).
const (
	lightMaxCores    = 2
	lightMaxRAMBytes = uint64(2) << 30
)

// Signals are the host facts detection reads. Zero means unknown.
type Signals struct {
	Cores    int
	RAMBytes uint64
	PiModel  string // device-tree model when this is a Raspberry Pi, else ""
	GOOS     string
	GOARCH   string
}

// Result is a detected level and its machine-readable reason
// (e.g. "pi,cores=4,ram=8g").
type Result struct {
	Level  string
	Reason string
}

// Test seams, one per signal.
var (
	numCPU      = runtime.NumCPU
	meminfoPath = "/proc/meminfo"
	piModel     = pihealth.PiModel
	hostGOOS    = runtime.GOOS
	hostGOARCH  = runtime.GOARCH
)

// ReadSignals reads this host's signals. RAM is read from /proc/meminfo on
// Linux and Android only; elsewhere it is unknown (0).
func ReadSignals() Signals {
	s := Signals{
		Cores:   numCPU(),
		PiModel: piModel(),
		GOOS:    hostGOOS,
		GOARCH:  hostGOARCH,
	}
	if s.GOOS == "linux" || s.GOOS == "android" {
		if b, err := os.ReadFile(meminfoPath); err == nil {
			s.RAMBytes, _ = parseMemTotal(b)
		}
	}
	return s
}

// parseMemTotal returns /proc/meminfo's MemTotal in bytes.
func parseMemTotal(b []byte) (uint64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || f[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || kb == 0 {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// ramGiB is the RAM rounded to whole GiB (at least 1), the unit the reason
// and the fingerprint bucket use: a "2 GB" board reports a little under
// 2 GiB of MemTotal and should read as 2.
func ramGiB(b uint64) uint64 {
	g := (b + (1 << 29)) >> 30
	if g == 0 {
		g = 1
	}
	return g
}

// Detect applies the host rule: Light on a Raspberry Pi, with ≤ 2 cores, or
// with ≤ 2 GiB of known RAM; Full otherwise. Unknown values are never a
// signal.
func Detect(s Signals) Result {
	light := s.PiModel != ""
	var reason []string
	if s.PiModel != "" {
		reason = append(reason, "pi")
	}
	if s.Cores > 0 {
		reason = append(reason, "cores="+strconv.Itoa(s.Cores))
		light = light || s.Cores <= lightMaxCores
	}
	if s.RAMBytes > 0 {
		reason = append(reason, "ram="+strconv.FormatUint(ramGiB(s.RAMBytes), 10)+"g")
		light = light || s.RAMBytes <= lightMaxRAMBytes
	}
	level := Full
	if light {
		level = Light
	}
	return Result{Level: level, Reason: strings.Join(reason, ",")}
}

// Fingerprint identifies the hardware a detection was made on: cores, RAM
// bucket, Pi model and OS/arch. A changed fingerprint at boot re-runs
// detection (a joined replica, a moved disk, a RAM upgrade).
func (s Signals) Fingerprint() string {
	ram := "unknown"
	if s.RAMBytes > 0 {
		ram = strconv.FormatUint(ramGiB(s.RAMBytes), 10) + "g"
	}
	model := s.PiModel
	if model == "" {
		model = "-"
	}
	return "cores=" + strconv.Itoa(s.Cores) + "|ram=" + ram + "|model=" + model + "|" + s.GOOS + "/" + s.GOARCH
}

// ReasonToken is one parsed reason entry: a flag ("pi") or a count
// ("cores" 4, "ram" 8 — GiB).
type ReasonToken struct {
	Name  string
	Value int
}

// ParseReason turns a stored reason into tokens the Settings page can put
// into words. Unknown or malformed tokens are dropped, never shown raw.
func ParseReason(reason string) []ReasonToken {
	var out []ReasonToken
	for _, tok := range strings.Split(reason, ",") {
		name, val, hasVal := strings.Cut(tok, "=")
		switch {
		case name == "pi" && !hasVal:
			out = append(out, ReasonToken{Name: "pi"})
		case name == "cores" && hasVal:
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				out = append(out, ReasonToken{Name: "cores", Value: n})
			}
		case name == "ram" && hasVal && strings.HasSuffix(val, "g"):
			if n, err := strconv.Atoi(strings.TrimSuffix(val, "g")); err == nil && n > 0 {
				out = append(out, ReasonToken{Name: "ram", Value: n})
			}
		}
	}
	return out
}
