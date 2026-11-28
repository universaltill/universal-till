package main

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/registrydesc"
)

// settingsRegistryJSONPath is the committed artefact, relative to the repo root.
const settingsRegistryJSONPath = "docs/till-settings-registry.json"

// registryEntryJSON is one artefact row. No values, ever: the artefact is
// the schema of the till's settings, not a shop's data.
type registryEntryJSON struct {
	Key                 string   `json:"key"`
	Type                string   `json:"type"`
	Tier                string   `json:"tier"`
	Secret              bool     `json:"secret"`
	Default             string   `json:"default"`
	On                  string   `json:"on"`
	Off                 string   `json:"off"`
	Min                 *int     `json:"min"`
	Max                 *int     `json:"max"`
	Options             []string `json:"options"`
	LabelKey            string   `json:"label_key"`
	DescriptionKey      string   `json:"description_key"`
	Region              string   `json:"region"`
	MinTillVersion      string   `json:"min_till_version"`
	Audit               string   `json:"audit"`
	WritableByTillAgent bool     `json:"writable_by_till_agent"`
	RemoteWritable      bool     `json:"remote_writable"`

	// Descriptor fields (design-B RC1), additive: ut-cloud builds the unified
	// shop configuration descriptor from them. All derived from the registry.
	Owner            string `json:"owner"`
	Scope            string `json:"scope"`
	Restorable       bool   `json:"restorable"`
	RestorableReason string `json:"restorable_reason"`
	ConfirmBy        string `json:"confirm_by"`
	StepUp           bool   `json:"step_up"`
	GateSwitch       string `json:"gate_switch"`

	// Class is the base visibility class of the key's region (ADR-0190),
	// from uislot. Additive; a shop-type template may demote it at runtime.
	Class string `json:"class"`
}

// registryClass returns the base class of a registry region, or an error when
// the region names no Settings card (the generator must fail, not guess).
func registryClass(region string) (string, error) {
	c, ok := registrydesc.ClassForRegion(region)
	if !ok {
		return "", fmt.Errorf("registry region %q has no visibility class (registrydesc.ClassForRegion)", region)
	}
	return string(c), nil
}

// settingsRegistryJSON renders the registry as the committed artefact:
// {"schema":1,"entries":[...]} in registry order, two-space indent, trailing
// newline. remote_writable is true only for what RemoteWritableKeys allows.
func settingsRegistryJSON() ([]byte, error) {
	remote := data.RemoteWritableKeys()
	rows := make([]registryEntryJSON, 0, len(data.AgentSettings))
	for _, s := range data.AgentSettings {
		r := registryEntryJSON{
			Key: s.Key, Type: s.Type, Tier: s.Tier, Secret: s.Secret, Default: s.Default,
			On: s.On, Off: s.Off, Options: slices.Clone(s.Options),
			LabelKey: s.LabelKey, DescriptionKey: s.HintKey, Region: s.Region,
			MinTillVersion: s.MinTillVersion, Audit: s.Audit,
			WritableByTillAgent: s.Writable, RemoteWritable: remote[s.Key],
			Owner: registrydesc.DescriptorOwnerCore, Scope: registrydesc.DescriptorScope(s),
			Restorable: registrydesc.Restorable(s) && remote[s.Key], RestorableReason: s.NotRestorableReason,
		}
		class, err := registryClass(s.Region)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Key, err)
		}
		r.Class = class
		gate := registrydesc.TierGateFor(s.Tier)
		r.ConfirmBy, r.StepUp, r.GateSwitch = gate.ConfirmBy, gate.StepUp, gate.Switch
		if s.Type == data.SettingTypeInt {
			mn, mx := s.Min, s.Max
			r.Min, r.Max = &mn, &mx
		}
		rows = append(rows, r)
	}
	b, err := json.MarshalIndent(struct {
		Schema               int                 `json:"schema"`
		ExpectMinTillVersion string              `json:"expect_min_till_version"`
		Entries              []registryEntryJSON `json:"entries"`
	}{1, data.RegistryMinTillVersion, rows}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
