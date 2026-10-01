package plugins

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// ADR-0121 §2 (ut-docs#3155): the plugin ABI a till implements, and the
// validation of the ABI-3 manifest fields. The fields parse today so the
// signing contract and the marketplace can carry them; the host functions
// that act on them land in ADR-0121 build cards 4–9.

const (
	// DefaultWasmABI is the ABI of a manifest without `wasm_abi` — the host
	// functions every till ships today.
	DefaultWasmABI = 2
	// MaxSupportedWasmABI is the newest ABI this till implements. A plugin
	// asking for more is refused, never loaded half-working. The build card
	// that completes the ABI-3 host functions raises it to 3.
	MaxSupportedWasmABI = 2
)

// EffectiveWasmABI is the manifest's ABI with the default applied.
func (m *Manifest) EffectiveWasmABI() int {
	if m.WasmABI == 0 {
		return DefaultWasmABI
	}
	return m.WasmABI
}

// Default resource requests (ADR-0121 §2) for a limit the manifest omits.
const (
	defaultLimitMemoryMB   = 64
	defaultLimitStorageMB  = 50
	defaultLimitHTTPBodyMB = 8
	// A long call (ADR-0121 §8) gets the platform ceiling unless it asks
	// for less: the ADR names no lower default.
	defaultLimitLongCallS = 300
)

// limitCeilings returns the platform ceilings for goos (ADR-0121 §2
// "Platform ceilings"; host constants, never manifest-overridable).
func limitCeilings(goos string) ManifestLimits {
	if goos == "android" || goos == "ios" {
		return ManifestLimits{MemoryMB: 128, StorageMB: 512, HTTPBodyMB: 32, LongCallS: 300}
	}
	return ManifestLimits{MemoryMB: 256, StorageMB: 2048, HTTPBodyMB: 64, LongCallS: 300}
}

// EffectiveLimits returns the limits the host grants on goos: each request
// (0 = default) clamped to the platform ceiling. It never mutates the
// manifest — the signed bytes are the request as published.
func (m *Manifest) EffectiveLimits(goos string) ManifestLimits {
	var req ManifestLimits
	if m.Limits != nil {
		req = *m.Limits
	}
	ceil := limitCeilings(goos)
	pick := func(v, def, max int) int {
		if v <= 0 {
			v = def
		}
		if v > max {
			v = max
		}
		return v
	}
	return ManifestLimits{
		MemoryMB:   pick(req.MemoryMB, defaultLimitMemoryMB, ceil.MemoryMB),
		StorageMB:  pick(req.StorageMB, defaultLimitStorageMB, ceil.StorageMB),
		HTTPBodyMB: pick(req.HTTPBodyMB, defaultLimitHTTPBodyMB, ceil.HTTPBodyMB),
		LongCallS:  pick(req.LongCallS, defaultLimitLongCallS, ceil.LongCallS),
	}
}

// contentSlots are the core content slots of ADR-0121 §7, in its order.
// A new slot needs an ADR amendment and a core release.
var contentSlots = []string{
	"item.edit.actions",
	"reports.panels",
	"setup.wizard.steps",
	"eod.footer",
	"settings.sections",
	"admin.pages",
}

func isContentSlot(s string) bool {
	for _, c := range contentSlots {
		if s == c {
			return true
		}
	}
	return false
}

var (
	// scheduleEventRe: at least two dot-separated lower-case segments
	// (`tax_de.tse_retry.tick`). The rule for which prefix a plugin may use
	// is ut-docs#3329; this is the shape only.
	scheduleEventRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)
	// coreViewNameRe: a versioned core read view (ADR-0121 §5),
	// `sales.by_day.v1`.
	coreViewNameRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*\.v[1-9][0-9]*$`)
	// pluginViewNameRe: a plugin's own view document name (`ask.home`).
	pluginViewNameRe = regexp.MustCompile(`^[a-z0-9_]+([.-][a-z0-9_]+)*$`)
	// localeKeyRe: a locale key from the plugin's own bundle.
	localeKeyRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)
)

// validateABI3Fields refuses a manifest whose ADR-0121 §2 fields are
// malformed or that needs an ABI this till does not implement. Called by
// ParseManifest and ManifestVerifier.VerifyManifest.
func validateABI3Fields(m *Manifest) error {
	switch {
	case m.WasmABI < 0 || m.WasmABI == 1:
		return fmt.Errorf("manifest wasm_abi %d is not a plugin ABI (omit it, or use %d..%d)", m.WasmABI, DefaultWasmABI, MaxSupportedWasmABI)
	case m.WasmABI > MaxSupportedWasmABI:
		return fmt.Errorf("plugin %s needs plugin ABI %d; this till supports up to %d — update the till to install it", m.ID, m.WasmABI, MaxSupportedWasmABI)
	}

	if l := m.Limits; l != nil {
		for _, f := range []struct {
			name string
			v    int
		}{{"memory_mb", l.MemoryMB}, {"storage_mb", l.StorageMB}, {"http_body_mb", l.HTTPBodyMB}, {"long_call_s", l.LongCallS}} {
			if f.v < 0 {
				return fmt.Errorf("manifest limits.%s must not be negative (got %d)", f.name, f.v)
			}
		}
	}

	for i, s := range m.Schedules {
		if !scheduleEventRe.MatchString(s.Event) {
			return fmt.Errorf("manifest schedules[%d].event %q must be dot-separated lower-case segments, like <plugin>.<name>", i, s.Event)
		}
		if s.EveryS < 1 {
			return fmt.Errorf("manifest schedules[%d].every_s must be at least 1 (got %d)", i, s.EveryS)
		}
		if s.JitterS < 0 {
			return fmt.Errorf("manifest schedules[%d].jitter_s must not be negative (got %d)", i, s.JitterS)
		}
	}

	if m.DB != nil {
		if err := validateMigrationsDir(m.DB.Migrations); err != nil {
			return err
		}
	}

	if r := m.Retention; r != nil {
		if r.KeepYears < 0 {
			return fmt.Errorf("manifest retention.keep_years must not be negative (got %d)", r.KeepYears)
		}
		if r.ReasonKey != "" && !localeKeyRe.MatchString(r.ReasonKey) {
			return fmt.Errorf("manifest retention.reason_key %q is not a locale key", r.ReasonKey)
		}
	}

	seen := make(map[string]bool, len(m.ViewsUsed))
	for _, v := range m.ViewsUsed {
		if !coreViewNameRe.MatchString(v) {
			return fmt.Errorf("manifest views_used %q is not a versioned view name like sales.by_day.v1", v)
		}
		if seen[v] {
			return fmt.Errorf("manifest views_used %q more than once", v)
		}
		seen[v] = true
	}

	for _, e := range m.Entries {
		if e.View != "" && !pluginViewNameRe.MatchString(e.View) {
			return fmt.Errorf("manifest entry %q view %q must be lower-case letters, digits, '_', '.' or '-'", e.Key, e.View)
		}
		if e.Slot != "" && !isContentSlot(e.Slot) {
			return fmt.Errorf("manifest entry %q slot %q is not a content slot (allowed: %s)", e.Key, e.Slot, strings.Join(contentSlots, "|"))
		}
	}
	return nil
}

// validateMigrationsDir requires a relative, forward-slash path that stays
// inside the plugin's code tree after cleaning.
func validateMigrationsDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("manifest db.migrations is required when db is declared")
	}
	if strings.ContainsAny(dir, "\\:\x00") {
		return fmt.Errorf("manifest db.migrations %q must be a relative path with '/' separators", dir)
	}
	clean := path.Clean(dir)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return fmt.Errorf("manifest db.migrations %q must be a directory inside the plugin", dir)
	}
	return nil
}
